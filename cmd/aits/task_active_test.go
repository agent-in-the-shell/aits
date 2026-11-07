package main

import (
	"os"
	"testing"
	"time"
)

// A running task's lease.active_at follows the agent's output; list shows it
// for running tasks; the beat file is gone once the run is recorded (#151).
func TestTaskActiveAt(t *testing.T) {
	q := newQueue(t, "slow_events")
	id := q.create("slow")
	w := call{path: q.path, env: []string{"AITS_HOME=" + q.home}, args: []string{"task", "work", "--json"}}.start(t)
	var first taskRecord
	q.waitShow(id, func(r taskRecord) bool { first = r; return r.Lease != nil && r.Lease.ActiveAt != "" })
	started, err := time.Parse(time.RFC3339Nano, first.Lease.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	a1, _ := time.Parse(time.RFC3339Nano, first.Lease.ActiveAt)
	if a1.Before(started.Add(-time.Second)) {
		t.Errorf("active_at %s before started_at %s", a1, started)
	}
	l := decodeOne[taskList](t, q.run("list", "--json").stdout)
	if len(l.Tasks) != 1 || l.Tasks[0].ActiveAt == "" {
		t.Errorf("list: %+v, want active_at on the running task", l.Tasks)
	}
	var second taskRecord
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		second = decodeOne[taskRecord](t, q.run("show", id, "--json").stdout)
		if second.Lease == nil || second.Lease.ActiveAt != first.Lease.ActiveAt {
			break
		}
	}
	if second.Lease == nil {
		t.Fatalf("the run ended before a second event was seen: %+v", second)
	}
	if a2, _ := time.Parse(time.RFC3339Nano, second.Lease.ActiveAt); !a2.After(a1.Add(4 * time.Second)) {
		t.Errorf("active_at %s, then %s: want it to move with the second event", a1, a2)
	}
	if out := w.waitOrKill(t, 20*time.Second); out.code != 0 {
		t.Fatalf("work: code=%d stderr=%q", out.code, out.stderr)
	}
	if _, err := os.Lstat(q.home + "/task/" + id + ".beat"); !os.IsNotExist(err) {
		t.Errorf("beat file after the run: %v", err)
	}
	if l := decodeOne[taskList](t, q.run("list", "--json").stdout); l.Tasks[0].ActiveAt != "" {
		t.Errorf("a done task lists active_at %q", l.Tasks[0].ActiveAt)
	}
	// A running task without a beat file (an older aits) shows none.
	old := q.create("old")
	q.setRunning(old, map[string]any{"token": "w", "pgid": 0})
	if r := decodeOne[taskRecord](t, q.run("show", old, "--json").stdout); r.Lease == nil || r.Lease.ActiveAt != "" {
		t.Errorf("no beat file: %+v", r.Lease)
	}
}
