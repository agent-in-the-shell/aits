package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeInbox writes files (path relative to dir → content) with an mtime
// old enough to have settled.
func writeInbox(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	old := time.Now().Add(-time.Minute)
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
}

// snapshot describes every entry under dir by its mode, mtime and bytes (or
// link target), so a test can assert serve changed nothing there.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		var body string
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			body, err = os.Readlink(path)
		case fi.Mode().IsRegular():
			var b []byte
			b, err = os.ReadFile(path)
			body = string(b)
		}
		snap[path] = fmt.Sprint(fi.Mode(), fi.ModTime().UnixNano(), body)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// inboxEvents returns the recorded inbox.file events by source id.
func (h *home) inboxEvents() map[string]map[string]string {
	h.t.Helper()
	rows, err := h.db().Query("SELECT source_id, payload, seen FROM events WHERE kind = 'inbox.file'")
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	events := map[string]map[string]string{}
	for rows.Next() {
		var id, payload string
		var seen int
		if err := rows.Scan(&id, &payload, &seen); err != nil {
			h.t.Fatal(err)
		}
		var p map[string]string
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			h.t.Fatal(err)
		}
		if seen != 1 {
			h.t.Errorf("inbox event %s seen %d times", id, seen)
		}
		events[id] = p
	}
	return events
}

// settled checks that every inbox event was copied at least 2s after its
// file's mtime (its put_at), so no file was read before it settled.
func (h *home) settled() {
	h.t.Helper()
	rows, err := h.db().Query("SELECT source_id, put_at, ingested_at FROM events WHERE kind = 'inbox.file'")
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, put, ingested string
		if err := rows.Scan(&id, &put, &ingested); err != nil {
			h.t.Fatal(err)
		}
		p, err1 := time.Parse(time.RFC3339Nano, put)
		i, err2 := time.Parse(time.RFC3339Nano, ingested)
		if err1 != nil || err2 != nil {
			h.t.Fatalf("%s: put_at %q, ingested_at %q", id, put, ingested)
		}
		if i.Sub(p) < 2*time.Second {
			h.t.Errorf("%s was copied %v after its mtime, before it settled", id, i.Sub(p))
		}
	}
}

// TestInboxReadOnly checks the adapter of #167: settled files under the inbox
// and its subdirectories become one event each, with their content; temp
// files, hidden directories, symlinks and unsettled files are not copied; and
// nothing under the inbox changes.
func TestInboxReadOnly(t *testing.T) {
	h := newHome(t)
	dir := t.TempDir()
	big := strings.Repeat("x", 32*1024+1)
	writeInbox(t, dir, map[string]string{
		"a.json":          `{"kind":"job"}`,
		"done/b.json":     `{"kind":"done"}`,
		"big.json":        big,
		"bin.json":        "\xff\xfe",
		".tmp-1":          "half",
		".hidden/c.json":  "{}",
		"done/.tmp-2":     "half",
		"nested/x/d.json": "d",
	})
	if err := os.Symlink(filepath.Join(dir, "a.json"), filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	// Written now: copied only after it settles.
	if err := os.WriteFile(filepath.Join(dir, "fresh.json"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, dir)

	s := h.serve("--inbox", dir)
	h.until("every file to be copied", func(st status) bool { return st.Events == 6 })
	out := s.stop(t)
	if out.code != 0 || !strings.Contains(out.stdout, `"ingested":6`) {
		t.Errorf("serve: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
	if n := strings.Count(out.stderr, "inbox file link.json skipped: not a regular file"); n != 1 {
		t.Errorf("link.json reported %d times, want once: %q", n, out.stderr)
	}

	want := map[string]map[string]string{
		"a.json":     {"name": "a.json", "dir": ".", "size": "14", "content": `{"kind":"job"}`},
		"b.json":     {"name": "b.json", "dir": "done", "size": "15", "content": `{"kind":"done"}`},
		"big.json":   {"name": "big.json", "dir": ".", "size": "32769", "content_omitted": "too_large"},
		"bin.json":   {"name": "bin.json", "dir": ".", "size": "2", "content_omitted": "not_utf8"},
		"d.json":     {"name": "d.json", "dir": filepath.Join("nested", "x"), "size": "1", "content": "d"},
		"fresh.json": {"name": "fresh.json", "dir": ".", "size": "3", "content": "new"},
	}
	if got := h.inboxEvents(); !reflect.DeepEqual(got, want) {
		t.Errorf("inbox events:\n got  %v\n want %v", got, want)
	}
	h.settled()
	if after := snapshot(t, dir); !maps.Equal(before, after) {
		t.Errorf("serve changed the inbox:\n before %v\n after  %v", before, after)
	}
}

// TestInboxMoved checks that a file the consumer moves into done/, before or
// after serve saw it, is one event, and that a restarted serve does not copy
// recorded names again.
func TestInboxMoved(t *testing.T) {
	h := newHome(t)
	dir := t.TempDir()
	writeInbox(t, dir, map[string]string{"a.json": "a", "done/early.json": "e"})
	s := h.serve("--inbox", dir)
	h.until("both files", func(st status) bool { return st.Events == 2 })
	if err := os.Rename(filepath.Join(dir, "a.json"), filepath.Join(dir, "done", "a.json")); err != nil {
		t.Fatal(err)
	}
	writeInbox(t, dir, map[string]string{"b.json": "b"})
	h.until("b.json", func(st status) bool { return st.Events == 3 })
	s.stop(t)

	s = h.serve("--inbox", dir)
	writeInbox(t, dir, map[string]string{"c.json": "c"})
	st := h.until("c.json after a restart", func(st status) bool { return st.Events == 4 })
	if st.Duplicates != 0 {
		t.Errorf("status = %+v, want no duplicates", st)
	}
	if out := s.stop(t); !strings.Contains(out.stdout, `"ingested":1`) {
		t.Errorf("restarted serve copied recorded files again: %q", out.stdout)
	}
	if got := h.inboxEvents()["a.json"]["dir"]; got != "." {
		t.Errorf("a.json dir = %q, want the directory it was first seen in", got)
	}
}

// TestInboxInvalid checks the --inbox failure cases of #167.
func TestInboxInvalid(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, arg := range map[string]string{
		"missing":   "--inbox=" + filepath.Join(dir, "nope"),
		"not a dir": "--inbox=" + file,
		"empty":     "--inbox=",
	} {
		h := newHome(t)
		wantError(t, name, h.run("serve", arg, "--json"), 2, "invalid_input")
		if h.status().Serving {
			t.Errorf("%s: serve still running", name)
		}
	}
}

// TestInboxRemoved checks that serve exits 1 with inbox, for its supervisor
// to restart it, when the inbox cannot be walked any more.
func TestInboxRemoved(t *testing.T) {
	h := newHome(t)
	dir := filepath.Join(t.TempDir(), "inbox")
	writeInbox(t, dir, map[string]string{"a.json": "a"})
	s := h.serve("--inbox", dir)
	h.until("a.json", func(st status) bool { return st.Events == 1 })
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-s.done:
		wantError(t, "serve", finish(t, err, s.stdout, s.stderr), 1, "inbox")
	case <-time.After(10 * time.Second):
		t.Fatal("serve kept running after its inbox was removed")
	}
}
