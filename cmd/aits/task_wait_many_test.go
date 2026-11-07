package main

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// waitedMany is the --json output of task wait with several ids (#147).
type waitedMany struct {
	Ended   []taskRecord `json:"ended"`
	Pending []string     `json:"pending"`
	Error   *errorBody   `json:"error"`
}

func ids(ts []taskRecord) []string {
	var s []string
	for _, t := range ts {
		s = append(s, t.ID)
	}
	return s
}

// waitMany starts "task wait --json IDS... [flags]".
func (q queue) waitMany(ids []string, flags ...string) *started {
	q.t.Helper()
	args := append(append([]string{"task", "wait", "--json"}, ids...), flags...)
	return call{path: q.path, env: []string{"AITS_HOME=" + q.home}, args: args}.start(q.t)
}

// --any returns once the first task ends, with the others pending; without
// it, wait returns once every task has ended, in the order they ended.
func TestTaskWaitAnyAndAll(t *testing.T) {
	q := newQueue(t, "ok")
	a, b, c := q.create("a"), q.create("b"), q.create("c")
	anyW := q.waitMany([]string{c, b, a}, "--any")
	allW := q.waitMany([]string{c, a})
	if out, ok := anyW.wait(t, 700*time.Millisecond); ok {
		t.Fatalf("wait --any returned before any task ended: code=%d stdout=%q", out.code, out.stdout)
	}
	if out := q.run("work"); out.code != 0 { // works a, the oldest
		t.Fatalf("work: code=%d stderr=%q", out.code, out.stderr)
	}
	out := anyW.waitOrKill(t, 10*time.Second)
	w := decodeOne[waitedMany](t, out.stdout)
	if out.code != 0 || w.Error != nil || !slices.Equal(ids(w.Ended), []string{a}) || w.Ended[0].Status != "done" || !slices.Equal(w.Pending, []string{c, b}) {
		t.Fatalf("--any: code=%d stdout=%s", out.code, out.stdout)
	}
	if out, ok := allW.wait(t, 500*time.Millisecond); ok {
		t.Fatalf("wait returned with c still queued: code=%d stdout=%q", out.code, out.stdout)
	}
	for range 2 {
		if out := q.run("work"); out.code != 0 {
			t.Fatalf("work: code=%d stderr=%q", out.code, out.stderr)
		}
	}
	out = allW.waitOrKill(t, 10*time.Second)
	w = decodeOne[waitedMany](t, out.stdout)
	if out.code != 0 || !slices.Equal(ids(w.Ended), []string{a, c}) || len(w.Pending) != 0 || w.Pending == nil {
		t.Errorf("all: code=%d stdout=%s", out.code, out.stdout)
	}
}

// A task already ended, cancelled or whose worker is gone counts as ended at
// once; a timeout exits 1 with what has ended and what is pending.
func TestTaskWaitManyEndsAndTimeout(t *testing.T) {
	q := newQueue(t, "ok")
	gone, cancelled, queued := q.create("gone"), q.create("cancel"), q.create("queued")
	if out := q.run("cancel", cancelled); out.code != 0 {
		t.Fatalf("cancel: code=%d stderr=%q", out.code, out.stderr)
	}
	q.setRunning(gone, map[string]any{"token": "w", "pgid": 0})
	q.writeLockFile(gone) // nobody holds it: the worker is gone

	out := q.waitMany([]string{gone, cancelled, queued}, "--timeout", "600ms").waitOrKill(t, 10*time.Second)
	w := decodeOne[waitedMany](t, out.stdout)
	if out.code != 1 || w.Error == nil || w.Error.Code != "timeout" || !strings.Contains(w.Error.Message, "1 of 3 tasks") ||
		len(w.Ended) != 2 || !slices.Equal(w.Pending, []string{queued}) {
		t.Fatalf("code=%d stdout=%s", out.code, out.stdout)
	}
	runs := map[string]string{}
	for _, e := range w.Ended {
		runs[e.ID] = e.Run.Status
	}
	if runs[gone] != "worker_died" || runs[cancelled] != "cancelled" {
		t.Errorf("ended runs %v", runs)
	}

	text := q.run("wait", gone, queued, "--any")
	if text.code != 0 || !strings.HasPrefix(text.stdout, gone+"  failed  ") || !strings.HasSuffix(text.stdout, queued+"  queued\n") {
		t.Errorf("text: code=%d stdout=%q stderr=%q", text.code, text.stdout, text.stderr)
	}
}

func TestTaskWaitManyRefuses(t *testing.T) {
	q := newQueue(t, "ok")
	a := q.create("a")
	for name, args := range map[string][]string{
		"repeated": {a, a},
		"unknown":  {a, "20000101T000000-000000"},
	} {
		out := q.run(append([]string{"wait", "--json"}, args...)...)
		if e := decode(t, out.stdout).Error; out.code != 2 || e == nil || e.Code != "invalid_input" {
			t.Errorf("%s: code=%d stdout=%q", name, out.code, out.stdout)
		}
	}
	// One id keeps the one-task output, --any or not.
	if out := q.run("wait", "--json", "--any", "--timeout", "100ms", a); decode(t, out.stdout).Error == nil || !strings.Contains(out.stdout, "is still queued after 100ms") {
		t.Errorf("one id with --any: code=%d stdout=%q", out.code, out.stdout)
	}
}

type taskEvent struct {
	At        time.Time `json:"at"`
	ID        string    `json:"id"`
	Event     string    `json:"event"`
	Status    string    `json:"status"`
	RunStatus string    `json:"run_status"`
	Label     string    `json:"label"`
}

func (q queue) events() []taskEvent {
	q.t.Helper()
	f, err := os.Open(filepath.Join(q.home, "task", "events.jsonl"))
	if err != nil {
		q.t.Fatal(err)
	}
	defer f.Close()
	var evs []taskEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		evs = append(evs, decodeOne[taskEvent](q.t, sc.Text()))
	}
	return evs
}

// Each transition appends one line to events.jsonl; removing the log breaks
// nothing (#147).
func TestTaskEvents(t *testing.T) {
	q := newQueue(t, "ok")
	a := q.create("a", "--label", "nightly")
	if out := q.run("work"); out.code != 0 {
		t.Fatalf("work: code=%d stderr=%q", out.code, out.stderr)
	}
	b := q.create("b")
	if out := q.run("cancel", b); out.code != 0 {
		t.Fatalf("cancel: code=%d", out.code)
	}
	var got []string
	for _, e := range q.events() {
		got = append(got, e.ID+" "+e.Event+" "+e.Status+" "+e.RunStatus+" "+e.Label)
		if e.At.IsZero() {
			t.Errorf("event without a time: %+v", e)
		}
	}
	want := []string{
		a + " created queued  nightly",
		a + " claimed running  nightly",
		a + " started running  nightly",
		a + " ended done ok nightly",
		b + " created queued  ",
		b + " cancelled failed cancelled ",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	if err := os.Remove(filepath.Join(q.home, "task", "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	c := q.create("c")
	if out := q.run("work"); out.code != 0 {
		t.Fatalf("work after removing the log: code=%d stderr=%q", out.code, out.stderr)
	}
	if evs := q.events(); len(evs) != 4 || evs[0].ID != c || evs[3].Event != "ended" {
		t.Errorf("events after removal: %+v", evs)
	}
}
