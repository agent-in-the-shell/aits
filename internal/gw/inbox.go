package gw

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agent-in-the-shell/aits/internal/store"
)

const (
	inboxKind  = "inbox.file"
	maxContent = 32 * 1024       // bytes of a file copied into its event
	settle     = 2 * time.Second // how old a file's mtime must be before it is copied
)

// inbox copies the files under a directory that another program owns into
// events (#167). It only reads: nothing under dir is created, moved,
// rewritten, removed or chmod'ed, so the program that consumes the directory
// sees exactly what it would without aits-gw.
type inbox struct {
	dir string
	// seen holds the names already recorded, reported or copied: each is read
	// at most once per serve.
	seen map[string]bool
}

// errInbox wraps a failure to walk the inbox; serve exits so its supervisor
// restarts it.
type errInbox struct{ err error }

func (e errInbox) Error() string { return "inbox: " + e.err.Error() }

// openInbox returns the inbox at dir, knowing every name already recorded.
func openInbox(ctx context.Context, db *sql.DB, dir string) (*inbox, error) {
	in := &inbox{dir: dir, seen: map[string]bool{}}
	rows, err := db.QueryContext(ctx, "SELECT source_id FROM events WHERE kind = ?", inboxKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		in.seen[name] = true
	}
	return in, rows.Err()
}

// scan records each file under the inbox that has settled and has not been
// seen, and returns how many it recorded. A file's source id is its name, so
// a file the consumer moved into a subdirectory is not a second event. A
// failure to walk the inbox is an errInbox.
func (in *inbox) scan(ctx context.Context, db *sql.DB, stderr io.Writer, now time.Time) (int, error) {
	n := 0
	err := filepath.WalkDir(in.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return errInbox{err}
		}
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		name := d.Name()
		switch {
		case path == in.dir:
			return nil
		case strings.HasPrefix(name, "."): // a writer's temp file, or hidden
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		case d.IsDir() || in.seen[name]:
			return nil
		}
		skip := func(why string) error {
			in.seen[name] = true
			fmt.Fprintf(stderr, "aits-gw: inbox file %s skipped: %s\n", name, why)
			return nil
		}
		if !d.Type().IsRegular() {
			return skip("not a regular file")
		}
		if why := validSource(name); why != "" {
			return skip("name " + why)
		}
		fi, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) || err == nil && now.Sub(fi.ModTime()) < settle {
			return nil // moved while we looked, or not settled: seen again later
		}
		var e *Event
		if err == nil {
			e, err = in.event(path, name, now)
		}
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil
		case err != nil:
			return skip(err.Error())
		}
		if err := record(ctx, db, e, now.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		in.seen[name] = true
		n++
		return nil
	})
	return n, err
}

// event reads the regular file at path, named name, into an event.
func (in *inbox) event(path, name string, now time.Time) (*Event, error) {
	f, fi, err := store.OpenRegular(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, maxContent+1))
	f.Close()
	if err != nil {
		return nil, err
	}
	dir, _ := filepath.Rel(in.dir, filepath.Dir(path))
	e := &Event{
		ID:       newID(now),
		Kind:     inboxKind,
		SourceID: name,
		Payload:  map[string]string{"name": name, "dir": dir, "size": strconv.FormatInt(fi.Size(), 10)},
		PutAt:    fi.ModTime().UTC().Format(time.RFC3339Nano),
	}
	switch {
	case len(b) > maxContent:
		e.Payload["content_omitted"] = "too_large"
	case !utf8.Valid(b):
		e.Payload["content_omitted"] = "not_utf8"
	default:
		e.Payload["content"] = string(b)
		// Escaping can grow content past the event limit (control characters
		// become \u00XX); then it is omitted, not truncated.
		if enc, _ := json.Marshal(e); len(enc) > maxPayload {
			delete(e.Payload, "content")
			e.Payload["content_omitted"] = "too_large"
		}
	}
	return e, nil
}
