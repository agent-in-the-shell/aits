package main

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

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

type checkEntry struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

func (h *home) check(dir string) checkResult {
	h.t.Helper()
	out := h.run("inbox", "check", "--inbox", dir, "--json")
	var r checkResult
	decode(h.t, out.stdout, &r)
	if out.code != 0 || out.stderr != "" {
		h.t.Fatalf("inbox check: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
	return r
}

// TestInboxCheck runs the acceptance of #169: a clean comparison, then one
// with a missed, a changed, a removed, a skipped and a pending file and both
// kinds of duplicate. The check changes nothing under the inbox.
func TestInboxCheck(t *testing.T) {
	h := newHome(t)
	dir := t.TempDir()
	writeInbox(t, dir, map[string]string{"a.json": "a", "done/b.json": "b", "e.json": "e", "f.json": "f"})
	s := h.serve("--inbox", dir)
	h.until("every file", func(st status) bool { return st.Events == 4 })
	s.stop(t)

	clean := checkResult{Status: "ok", Files: 4, Events: 4, Matched: 4, Missed: []string{}, Extra: []checkEntry{}, Duplicates: []checkEntry{}}
	if got := h.check(dir); !reflect.DeepEqual(got, clean) {
		t.Errorf("clean check = %+v, want %+v", got, clean)
	}

	writeInbox(t, dir, map[string]string{"c.json": "c", "a.json": "A", "done/e.json": "e2"})
	if err := os.Remove(filepath.Join(dir, "done", "b.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "c.json"), filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "d.json"), []byte("d"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db().Exec("UPDATE events SET seen = 2 WHERE source_id = 'f.json'"); err != nil {
		t.Fatal(err)
	}

	before := snapshot(t, dir)
	want := checkResult{
		Status: "mismatch", Files: 6, Events: 4, Matched: 0, Pending: 1, Skipped: 1,
		Missed:     []string{"c.json"},
		Extra:      []checkEntry{{"a.json", "changed"}, {"b.json", "no_file"}},
		Duplicates: []checkEntry{{"e.json", "files"}, {"f.json", "seen"}},
	}
	if got := h.check(dir); !reflect.DeepEqual(got, want) {
		t.Errorf("check = %+v\nwant  %+v", got, want)
	}
	if after := snapshot(t, dir); !maps.Equal(before, after) {
		t.Errorf("check changed the inbox:\n before %v\n after  %v", before, after)
	}

	out := h.run("inbox", "check", "--inbox", dir)
	for _, line := range []string{"status: mismatch\n", "missed: 1\n  c.json\n", "extra: 2\n  a.json  changed\n  b.json  no_file\n", "duplicates: 2\n"} {
		if !strings.Contains(out.stdout, line) {
			t.Errorf("text output lacks %q: %q", line, out.stdout)
		}
	}
}

func TestInboxCheckInvalid(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"no inbox":  {},
		"empty":     {"--inbox="},
		"not a dir": {"--inbox", file},
		"missing":   {"--inbox", file + "-nope"},
		"argument":  {"--inbox", filepath.Dir(file), "extra"},
	} {
		out := newHome(t).run(append([]string{"inbox", "check", "--json"}, args...)...)
		wantError(t, name, out, 2, "invalid_input")
	}
}
