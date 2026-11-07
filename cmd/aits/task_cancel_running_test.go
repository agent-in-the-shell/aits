package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func (q queue) stopFile(id string) string { return filepath.Join(q.home, "task", id+".stop") }

// cancel stops a running task through its worker: the agent's group is gone,
// the task is failed with run status stopped, and the worker exits as after
// any failed run (#149).
func TestTaskCancelStopsRunning(t *testing.T) {
	q := newQueue(t, "sleep")
	id := q.create("long")
	w := call{path: q.path, env: []string{"AITS_HOME=" + q.home}, args: []string{"task", "work", "--json"}}.start(t)
	waitFile(t, filepath.Join(q.path, "child.pid"))
	pgid := readPID(t, filepath.Join(q.path, "child.pid"))
	q.waitLease(id, pgid)
	grandchild := readPID(t, filepath.Join(q.path, "grandchild.pid"))

	start := time.Now()
	out := q.run("cancel", id, "--json")
	if took := time.Since(start); took > 7*time.Second {
		t.Errorf("cancel took %s, want within the 5s grace and a poll", took)
	}
	r := decodeOne[taskRecord](t, out.stdout)
	if out.code != 0 || r.Status != "failed" || r.Run == nil || r.Run.Status != "stopped" || r.Run.Message != "stopped by task cancel" {
		t.Fatalf("cancel: code=%d stdout=%s", out.code, out.stdout)
	}
	assertGone(t, pgid)
	assertGone(t, grandchild)
	if wo := w.waitOrKill(t, 10*time.Second); wo.code != 1 || !strings.Contains(wo.stdout, `"stopped by task cancel"`) {
		t.Errorf("work: code=%d stdout=%q", wo.code, wo.stdout)
	}
	if _, err := os.Lstat(q.stopFile(id)); !os.IsNotExist(err) {
		t.Errorf("stop request left behind: %v", err)
	}
	evs := q.events()
	if last := evs[len(evs)-1]; last.ID != id || last.Event != "ended" || last.RunStatus != "stopped" {
		t.Errorf("last event %+v, want ended stopped", last)
	}
}

// A running task whose worker is gone is reclaimed, not sent a request.
func TestTaskCancelDeadWorker(t *testing.T) {
	q := newQueue(t, "ok")
	id := q.create("dead")
	q.deadWorker(id, `: > "$0"; exec sleep 60`)
	out := q.run("cancel", id, "--json")
	if r := decodeOne[taskRecord](t, out.stdout); out.code != 0 || r.Status != "failed" || r.Run == nil || r.Run.Status != "worker_died" {
		t.Fatalf("cancel: code=%d stdout=%s", out.code, out.stdout)
	}
	if _, err := os.Lstat(q.stopFile(id)); !os.IsNotExist(err) {
		t.Errorf("stop request written for a dead worker: %v", err)
	}
}

// A worker that does not act on the request: cancel times out with exit 1,
// leaves the task running and the request in place.
func TestTaskCancelTimeout(t *testing.T) {
	q := newQueue(t, "ok")
	id := q.create("held")
	q.setRunning(id, map[string]any{"token": "live", "pgid": 0})
	q.writeLockFile(id)
	f, err := os.OpenFile(q.lockFile(id), os.O_RDWR, 0)
	if err == nil {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX) // a live worker that never looks for the request
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := q.run("cancel", id, "--json", "--timeout", "1s")
	if e := decode(t, out.stdout).Error; out.code != 1 || e == nil || e.Code != "timeout" || !strings.Contains(e.Message, "has the stop request") {
		t.Fatalf("cancel: code=%d stdout=%q", out.code, out.stdout)
	}
	if r := decodeOne[taskRecord](t, q.run("show", id, "--json").stdout); r.Status != "running" {
		t.Errorf("task %+v, want still running", r)
	}
	if _, err := os.Lstat(q.stopFile(id)); err != nil {
		t.Errorf("stop request: %v, want it kept", err)
	}
}
