package main

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// peak is the most tasks running at once, from the event log (#147).
func (q queue) peak() int {
	q.t.Helper()
	n, top := 0, 0
	for _, e := range q.events() {
		switch e.Event {
		case "claimed":
			n++
			top = max(top, n)
		case "ended":
			n--
		}
	}
	return top
}

// --jobs 2 works four tasks two at a time and prints each as it ends (#155).
func TestTaskWorkJobs(t *testing.T) {
	q := newQueue(t, "brief")
	for range 4 {
		q.create("x")
	}
	out := q.run("work", "--jobs", "2", "--json")
	lines := strings.Split(strings.TrimSpace(out.stdout), "\n")
	if out.code != 0 || len(lines) != 4 {
		t.Fatalf("code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
	for _, l := range lines {
		if r := decodeOne[taskRecord](t, l); r.Status != "done" {
			t.Errorf("task %+v", r)
		}
	}
	if p := q.peak(); p != 2 {
		t.Errorf("peak %d running, want 2", p)
	}
	if out := q.run("work", "--jobs", "2", "--json"); out.code != 0 || strings.TrimSpace(out.stdout) != `{"status":"empty"}` {
		t.Errorf("empty queue: code=%d stdout=%q", out.code, out.stdout)
	}
}

// One failing task makes --jobs exit 1; the others still run.
func TestTaskWorkJobsFailure(t *testing.T) {
	q := newQueue(t, "brief")
	cwd := t.TempDir()
	bad := created(t, q.run("create", "--agent", "claude", "--cwd", cwd, "--json", "x"))
	if err := os.Remove(cwd); err != nil {
		t.Fatal(err)
	}
	q.create("ok1")
	q.create("ok2")
	out := q.run("work", "--jobs", "2", "--json")
	if out.code != 1 || strings.Count(out.stdout, "\n") != 3 {
		t.Fatalf("code=%d stdout=%q", out.code, out.stdout)
	}
	if r := decodeOne[taskRecord](t, q.run("show", bad, "--json").stdout); r.Status != "failed" {
		t.Errorf("bad task %+v", r)
	}
}

// --max-running counts running tasks across workers, by label if given.
func TestTaskWorkMaxRunning(t *testing.T) {
	q := newQueue(t, "ok")
	held := q.create("held", "--label", "x")
	q.setRunning(held, map[string]any{"token": "live", "pgid": 0})
	q.writeLockFile(held)
	f, err := os.OpenFile(q.lockFile(held), os.O_RDWR, 0)
	if err == nil {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX) // a live worker of another process
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	q.create("next", "--label", "x")
	q.create("other", "--label", "y")
	if out := q.run("work", "--max-running", "1", "--label", "x", "--json"); out.code != 0 || strings.TrimSpace(out.stdout) != `{"status":"empty"}` {
		t.Errorf("at the cap: code=%d stdout=%q", out.code, out.stdout)
	}
	if out := q.run("work", "--max-running", "1", "--label", "y", "--json"); out.code != 0 || decodeOne[taskRecord](t, out.stdout).Status != "done" {
		t.Errorf("another label: code=%d stdout=%q", out.code, out.stdout)
	}
	if out := q.run("work", "--max-running", "2", "--label", "x", "--json"); out.code != 0 || decodeOne[taskRecord](t, out.stdout).Status != "done" {
		t.Errorf("under the cap: code=%d stdout=%q", out.code, out.stdout)
	}
}

// A signal stops every running agent of --jobs and claims nothing more.
func TestTaskWorkJobsStopped(t *testing.T) {
	q := newQueue(t, "hold")
	for range 3 {
		q.create("long")
	}
	s := call{path: q.path, env: []string{"AITS_HOME=" + q.home}, args: []string{"task", "work", "--jobs", "2", "--json"}}.start(t)
	deadline := time.Now().Add(10 * time.Second)
	for running := 0; running < 2; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("two tasks never ran")
		}
		running = 0
		for _, i := range decodeOne[taskList](t, q.run("list", "--json").stdout).Tasks {
			if i.Status == "running" && i.ActiveAt != "" {
				running++
			}
		}
	}
	s.signal(t, syscall.SIGTERM)
	out := s.waitOrKill(t, 15*time.Second)
	if out.code != 1 || strings.Count(out.stdout, `"status":"stopped"`) != 2 {
		t.Fatalf("code=%d stdout=%q", out.code, out.stdout)
	}
	queued := 0
	for _, i := range decodeOne[taskList](t, q.run("list", "--json").stdout).Tasks {
		if i.Status == "queued" {
			queued++
		}
	}
	if queued != 1 {
		t.Errorf("%d queued after the stop, want the third untouched: %s", queued, q.run("list", "--json").stdout)
	}
}
