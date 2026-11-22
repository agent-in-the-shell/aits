package gw

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/agent-in-the-shell/aits/internal/store"
)

// pollEvery is how often serve reads the spool.
const pollEvery = 500 * time.Millisecond

// paths are the gateway's files under $AITS_HOME/gw (P7).
type paths struct {
	dir      string // $AITS_HOME/gw: gw.db and serve.lock
	spool    string // events put and not yet recorded
	rejected string // spool files serve could not read as an event
}

// resolve returns the gateway's directories, creating them 0700.
func resolve(environ []string) (paths, error) {
	var p paths
	var err error
	if p.dir, err = store.Dir(environ, "gw"); err != nil {
		return p, err
	}
	if p.spool, err = store.Dir(environ, filepath.Join("gw", "spool")); err != nil {
		return p, err
	}
	p.rejected, err = store.Dir(environ, filepath.Join("gw", "spool", "rejected"))
	return p, err
}

func (p paths) lock() string { return filepath.Join(p.dir, "serve.lock") }

// put writes e to the spool through a temporary file and a rename (P7), so
// serve sees the whole event or none of it.
func put(p paths, e *Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(b) > maxPayload {
		return errTooLarge
	}
	return store.WriteFile(filepath.Join(p.spool, e.ID+".json"), b)
}

var errTooLarge = fmt.Errorf("event is larger than %d bytes", maxPayload)

// spooled returns the event files in dir, oldest first. Temporary files start
// with "." and are skipped.
func spooled(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if n := e.Name(); e.Type().IsRegular() && !strings.HasPrefix(n, ".") && strings.HasSuffix(n, ".json") {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return names, nil
}

// ingest records every spooled event and removes its file, and returns how
// many it recorded. A file that is not an event is moved to rejected and
// reported on stderr.
func ingest(ctx context.Context, db *sql.DB, p paths, stderr io.Writer) (int, error) {
	names, err := spooled(p.spool)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, name := range names {
		if ctx.Err() != nil {
			break
		}
		path := filepath.Join(p.spool, name)
		f, _, err := store.OpenRegular(path, os.O_RDONLY, 0)
		if err != nil {
			return n, err
		}
		b, err := io.ReadAll(io.LimitReader(f, maxPayload+1))
		f.Close()
		if err != nil {
			return n, err
		}
		e, perr := parseEvent(b)
		if perr != nil {
			fmt.Fprintf(stderr, "aits-gw: rejected spool file %s: %v\n", name, perr)
			if err := store.WriteFile(filepath.Join(p.rejected, name), b); err != nil {
				return n, err
			}
		} else if err := record(ctx, db, e, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return n, err
		} else {
			n++
		}
		if err := store.Remove(path); err != nil {
			return n, err
		}
	}
	return n, nil
}

// errServing means another serve holds the lock.
var errServing = errors.New("another aits-gw serve is running")

// serve holds the serve lock and ingests the spool every pollEvery until ctx
// ends; it returns how many events it recorded. The lock is a flock the
// kernel releases when serve exits, so a crash leaves nothing to repair
// (P10).
func serve(ctx context.Context, p paths, stderr io.Writer) (int, error) {
	unlock, err := store.TryLock(p.lock(), true)
	if errors.Is(err, store.ErrLocked) {
		return 0, errServing
	}
	if err != nil {
		return 0, err
	}
	defer unlock()
	db, err := openDB(ctx, p.dir)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	total := 0
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		n, err := ingest(ctx, db, p, stderr)
		total += n
		if err != nil && ctx.Err() == nil {
			return total, err
		}
		select {
		case <-ctx.Done():
			return total, nil
		case <-tick.C:
		}
	}
}

// serving reports whether a serve holds the lock now.
func serving(p paths) (bool, error) {
	unlock, err := store.TryLock(p.lock(), false)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	case errors.Is(err, store.ErrLocked):
		return true, nil
	case err != nil:
		return false, err
	}
	unlock()
	return false, nil
}
