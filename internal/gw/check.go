package gw

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"slices"
	"time"
)

// pendingAge is how young an unrecorded file may be and still count as
// pending, not missed: the settle time plus a few polls.
const pendingAge = 10 * time.Second

// checkEntry is one name in a list of inbox check, with why it is there.
type checkEntry struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// checkResult is what inbox check prints (#169).
type checkResult struct {
	Status     string       `json:"status"`
	Files      int          `json:"files"`
	Events     int          `json:"events"`
	Matched    int          `json:"matched"`
	Pending    int          `json:"pending"`
	Skipped    int          `json:"skipped"`
	Missed     []string     `json:"missed"`
	Extra      []checkEntry `json:"extra"`
	Duplicates []checkEntry `json:"duplicates"`
}

// check compares the files under dir, walked as the adapter walks them, with
// the inbox.file events in the database, by name. It only reads dir.
func check(ctx context.Context, environ []string, dir string, now time.Time) (checkResult, error) {
	r := checkResult{Status: "ok", Missed: []string{}, Extra: []checkEntry{}, Duplicates: []checkEntry{}}
	files := map[string][]string{} // name → paths
	young := map[string]bool{}
	err := walkInbox(dir, func(path string, d fs.DirEntry) error {
		if uncopyable(d) != "" {
			r.Skipped++
			return nil
		}
		fi, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) { // moved while we looked
			return nil
		}
		if err != nil {
			return errInbox{err}
		}
		r.Files++
		files[d.Name()] = append(files[d.Name()], path)
		young[d.Name()] = young[d.Name()] || now.Sub(fi.ModTime()) < pendingAge
		return nil
	})
	if err != nil {
		return r, err
	}

	p, err := resolve(environ)
	if err != nil {
		return r, err
	}
	db, err := openDB(ctx, p.dir)
	if err != nil {
		return r, err
	}
	defer db.Close()
	// In source_id order, and "files" before "seen", so the lists come out
	// sorted.
	rows, err := db.QueryContext(ctx, "SELECT source_id, payload, seen FROM events WHERE kind = ? ORDER BY source_id", inboxKind)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	in := &inbox{dir: dir}
	for rows.Next() {
		var name, payload string
		var seen int
		if err := rows.Scan(&name, &payload, &seen); err != nil {
			return r, err
		}
		r.Events++
		problems := len(r.Extra) + len(r.Duplicates)
		paths := files[name]
		delete(files, name)
		if len(paths) > 1 {
			r.Duplicates = append(r.Duplicates, checkEntry{name, "files"})
		}
		if seen > 1 {
			r.Duplicates = append(r.Duplicates, checkEntry{name, "seen"})
		}
		switch {
		case len(paths) == 0:
			r.Extra = append(r.Extra, checkEntry{name, "no_file"})
		case len(paths) == 1:
			var recorded map[string]string
			if err := json.Unmarshal([]byte(payload), &recorded); err != nil {
				return r, err
			}
			// The file copied again now, compared with the copy recorded;
			// dir is where it was seen, which may have changed.
			e, err := in.event(paths[0], name, now)
			if err != nil {
				return r, errInbox{err}
			}
			delete(e.Payload, "dir")
			delete(recorded, "dir")
			if !maps.Equal(e.Payload, recorded) {
				r.Extra = append(r.Extra, checkEntry{name, "changed"})
			}
		}
		if len(r.Extra)+len(r.Duplicates) == problems {
			r.Matched++
		}
	}
	if err := rows.Err(); err != nil {
		return r, err
	}
	for name := range files { // files with no event
		if young[name] {
			r.Pending++
		} else {
			r.Missed = append(r.Missed, name)
		}
	}
	slices.Sort(r.Missed)
	if len(r.Missed)+len(r.Extra)+len(r.Duplicates) > 0 {
		r.Status = "mismatch"
	}
	return r, nil
}
