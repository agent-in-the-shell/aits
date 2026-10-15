package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// taskRecord is the task JSON of show and work; decoding rejects any field
// not listed here, so the shape is checked too (P5).
type taskRecord struct {
	ID        string   `json:"id"`
	Status    string   `json:"status"`
	Agent     string   `json:"agent"`
	Cwd       string   `json:"cwd"`
	Prompt    string   `json:"prompt"`
	TimeoutMS int64    `json:"timeout_ms"`
	Env       []string `json:"env"`
	After     string   `json:"after"`
	CreatedAt string   `json:"created_at"`
	Lease     *struct {
		Token     string `json:"token"`
		PGID      int    `json:"pgid"`
		StartedAt string `json:"started_at"`
	} `json:"lease"`
	Run *struct {
		Status     string `json:"status"`
		Agent      string `json:"agent"`
		SessionID  string `json:"session_id"`
		Answer     string `json:"answer"`
		ExitCode   int    `json:"exit_code"`
		DurationMS int64  `json:"duration_ms"`
		StderrTail string `json:"stderr_tail"`
		Message    string `json:"message"`
		StartedAt  string `json:"started_at"`
		FinishedAt string `json:"finished_at"`
	} `json:"run"`
}

// taskList is the --json output of task list; decoding rejects any field not
// listed here (P5).
type taskList struct {
	Tasks   []taskListItem `json:"tasks"`
	Skipped []string       `json:"skipped"`
}

type taskListItem struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Agent     string `json:"agent"`
	Cwd       string `json:"cwd"`
	CreatedAt string `json:"created_at"`
	After     string `json:"after"`
}

// queue is a shared AITS_HOME and fake-agent PATH for one test.
type queue struct {
	t          *testing.T
	home, path string
}

// newQueue makes a queue whose PATH holds the fake agent set to scenario, or
// no agent when scenario is "".
func newQueue(t *testing.T, scenario string) queue {
	q := queue{t: t, home: t.TempDir()}
	if scenario != "" {
		q.path = vendor(t, scenario)
	}
	return q
}

func (q queue) run(args ...string) output {
	q.t.Helper()
	return call{path: q.path, env: []string{"AITS_HOME=" + q.home}, args: append([]string{"task"}, args...)}.run(q.t)
}

// writeTaskFile writes body as the task file of id, as something other than
// aits would.
func (q queue) writeTaskFile(id, body string, mode os.FileMode) string {
	q.t.Helper()
	file := q.taskFile(id)
	if err := os.WriteFile(file, []byte(body), mode); err != nil {
		q.t.Fatal(err)
	}
	return file
}

// taskFile is the path of task id's record.
func (q queue) taskFile(id string) string { return filepath.Join(q.home, "task", id+".json") }

// lockFile is the path of task id's lock file.
func (q queue) lockFile(id string) string { return filepath.Join(q.home, "task", id+".lock") }

// writeLockFile leaves the lock file of id as a claim does, unlocked.
func (q queue) writeLockFile(id string) {
	q.t.Helper()
	if err := os.WriteFile(q.lockFile(id), nil, 0o600); err != nil {
		q.t.Fatal(err)
	}
}

// setRunning rewrites task id as running with lease, as a worker (or an
// earlier aits) would have left it, and returns the bytes written.
func (q queue) setRunning(id string, lease map[string]any) []byte {
	q.t.Helper()
	return q.patch(id, map[string]any{"status": "running", "lease": lease})
}

// patch sets fields of task id's record on disk and returns the bytes written.
func (q queue) patch(id string, fields map[string]any) []byte {
	q.t.Helper()
	file := q.taskFile(id)
	var rec map[string]any
	b, err := os.ReadFile(file)
	if err == nil {
		err = json.Unmarshal(b, &rec)
	}
	if err != nil {
		q.t.Fatal(err)
	}
	maps.Copy(rec, fields)
	b, _ = json.Marshal(rec)
	if err := os.WriteFile(file, b, 0o600); err != nil {
		q.t.Fatal(err)
	}
	return b
}

// waitLease waits until task id records the agent's process group pgid.
func (q queue) waitLease(id string, pgid int) {
	q.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if r := decodeOne[taskRecord](q.t, q.run("show", id, "--json").stdout); r.Lease != nil && r.Lease.PGID == pgid {
			return
		}
	}
	q.t.Fatalf("task %s never recorded pgid %d", id, pgid)
}

// queuedAny reports whether any task record exists.
func (q queue) queuedAny() bool { return q.count() > 0 }

// count returns how many task records exist.
func (q queue) count() int {
	m, _ := filepath.Glob(filepath.Join(q.home, "task", "*.json"))
	return len(m)
}

func (q queue) create(prompt string, flags ...string) string {
	q.t.Helper()
	return created(q.t, q.run(append(append([]string{"create", "--agent", "claude", "--cwd", q.t.TempDir(), "--json"}, flags...), prompt)...))
}

// after queues a follow-up of parent with flags and returns its id.
func (q queue) after(parent, prompt string, flags ...string) string {
	q.t.Helper()
	return created(q.t, q.run(append(append([]string{"create", "--after", parent, "--json"}, flags...), prompt)...))
}

// created returns the id that a successful create --json printed.
func created(t *testing.T, out output) string {
	t.Helper()
	var r struct{ ID, Status string }
	if err := json.Unmarshal([]byte(out.stdout), &r); out.code != 0 || err != nil || r.Status != "queued" {
		t.Fatalf("create: code=%d stdout=%q", out.code, out.stdout)
	}
	return r.ID
}
func TestTaskFlow(t *testing.T) {
	q := newQueue(t, "ok")
	id := q.create("Review the code")
	if out := q.run("list", "--json"); !strings.Contains(out.stdout, id) || !strings.Contains(out.stdout, `"queued"`) {
		t.Fatalf("list: %q", out.stdout)
	}
	out := q.run("work", "--json")
	w := decodeOne[taskRecord](t, out.stdout)
	if out.code != 0 || w.ID != id || w.Status != "done" || w.Run == nil || w.Run.Answer != "fake answer" ||
		w.Run.SessionID != "sess-fake-1" || w.Run.StartedAt == "" || w.Lease != nil {
		t.Fatalf("work: code=%d %+v", out.code, w)
	}
	if rec := readRecord(t, q.path); rec.Stdin != "Review the code" || slices.Contains(rec.Args, "Review the code") {
		t.Errorf("agent got stdin %q args %q", rec.Stdin, rec.Args)
	}
	if s := decodeOne[taskRecord](t, q.run("show", id, "--json").stdout); s.Status != "done" || s.Run.Answer != "fake answer" {
		t.Errorf("show: %+v", s)
	}
	if out := q.run("work", "--json"); out.code != 0 || out.stdout != `{"status":"empty"}`+"\n" {
		t.Errorf("empty queue: code=%d stdout=%q", out.code, out.stdout)
	}
	if out := q.run("work"); out.code != 0 || out.stdout != "" {
		t.Errorf("empty queue, text: code=%d stdout=%q", out.code, out.stdout)
	}
}

func TestTaskInvalidInput(t *testing.T) {
	q := newQueue(t, "ok")
	for name, args := range map[string][]string{
		"no agent":     {"create", "--cwd", t.TempDir(), "hi"},
		"empty prompt": {"create", "--agent", "claude", "--cwd", t.TempDir(), " "},
		"missing cwd":  {"create", "--agent", "claude", "--cwd", "/nonexistent/aits", "hi"},
		"unknown id":   {"show", "20000101T000000-000000"},
		"two ids":      {"show", "a", "b"},
	} {
		out := q.run(append(args, "--json")...)
		if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "invalid_input" {
			t.Errorf("%s: code=%d stdout=%q", name, out.code, out.stdout)
		}
	}
	if q.queuedAny() {
		t.Errorf("invalid input wrote a task")
	}
}

// A task stores its timeout in whole milliseconds: create refuses a value it
// cannot keep, quoting the value given, and keeps a whole one exactly (P6, #59).
func TestTaskCreateTimeoutMilliseconds(t *testing.T) {
	q := newQueue(t, "ok")
	for timeout, want := range map[string]string{
		"500us":   "a task's timeout must be a whole number of milliseconds, got 500µs",
		"1500us":  "a task's timeout must be a whole number of milliseconds, got 1.5ms",
		"-1500us": "timeout must be positive, got -1.5ms",
	} {
		out := q.run("create", "--agent", "claude", "--cwd", t.TempDir(), "--timeout", timeout, "--json", "hi")
		if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "invalid_input" || r.Error.Message != want {
			t.Errorf("%s: code=%d stdout=%q; want message %q", timeout, out.code, out.stdout, want)
		}
	}
	if q.queuedAny() {
		t.Fatalf("a timeout that cannot be stored was queued")
	}
	id := q.create("hi", "--timeout", "2ms")
	if s := decodeOne[taskRecord](t, q.run("show", id, "--json").stdout); s.TimeoutMS != 2 {
		t.Errorf("timeout_ms = %d, want 2", s.TimeoutMS)
	}
}

// A recorded run without a status is refused rather than printed, in both
// modes (P6, #61).
func TestTaskShowRunWithoutStatus(t *testing.T) {
	q := newQueue(t, "")
	id := q.create("hi")
	q.patch(id, map[string]any{"status": "failed", "run": map[string]any{"agent": "claude"}})
	if out := q.run("show", id); out.code != 1 || out.stdout != "" || !strings.HasPrefix(out.stderr, "aits: ") {
		t.Errorf("text: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
	out := q.run("show", id, "--json")
	if e := decode(t, out.stdout).Error; out.code != 1 || e == nil || e.Code != "internal" {
		t.Errorf("json: code=%d stdout=%q", out.code, out.stdout)
	}
}

// A run whose agent never started has no started_at (help, spec §4).
func TestTaskNotStartedHasNoStartedAt(t *testing.T) {
	q := newQueue(t, "ok")
	q.create("hi", "--env", "AITS_TEST_UNSET")
	out := q.run("work", "--json")
	if w := decodeOne[taskRecord](t, out.stdout); out.code != 1 || w.Run == nil || w.Run.Status != "not_started" {
		t.Fatalf("work: code=%d %+v", out.code, w)
	}
	// No exit code or duration: an agent that never ran has neither (P6, #57).
	if got := recordKeys(t, out.stdout, "run"); !slices.Equal(got, []string{"agent", "finished_at", "message", "status"}) {
		t.Errorf("run keys = %v", got)
	}
}

// recordKeys returns the sorted keys of field ("run" or "lease") of a task
// record.
func recordKeys(t *testing.T, stdout, field string) []string {
	t.Helper()
	var r map[string]json.RawMessage
	var f map[string]any
	err := json.Unmarshal([]byte(stdout), &r)
	if err == nil {
		err = json.Unmarshal(r[field], &f)
	}
	if err != nil {
		t.Fatal(err)
	}
	keys := slices.Collect(maps.Keys(f))
	slices.Sort(keys)
	return keys
}

// Claims are atomic: as many workers as tasks, started together, run each
// task exactly once.
func TestTaskConcurrentWorkers(t *testing.T) {
	q := newQueue(t, "ok")
	var ids []string
	for i := range 5 {
		ids = append(ids, q.create("task "+string(rune('a'+i))))
	}
	var mu sync.Mutex
	var worked []string
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			w := decodeOne[taskRecord](t, q.run("work", "--json").stdout)
			mu.Lock()
			worked = append(worked, w.ID)
			mu.Unlock()
		})
	}
	wg.Wait()
	slices.Sort(worked)
	slices.Sort(ids)
	if !slices.Equal(worked, ids) {
		t.Errorf("worked %q, want each of %q once", worked, ids)
	}
}

// A running task whose lock nobody holds is failed by the next list, even
// with an old lease whose pid is alive, and its leftover agent group is
// stopped (P10).
func TestTaskReclaim(t *testing.T) {
	q := newQueue(t, "ok")
	id := q.create("hi")
	sleeper := exec.Command("sleep", "60")
	sleeper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = sleeper.Wait() }()
	q.setRunning(id, map[string]any{"token": "dead", "worker_pid": os.Getpid(), "pgid": sleeper.Process.Pid,
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	q.writeLockFile(id)
	// An unreadable file next to it does not stop the reclaim.
	q.writeTaskFile("20261014T000000-bad000", "garbage", 0o600)
	if out := q.run("list"); out.code != 0 {
		t.Fatalf("list: code=%d stderr=%q", out.code, out.stderr)
	}
	if s := decodeOne[taskRecord](t, q.run("show", id, "--json").stdout); s.Status != "failed" || s.Run == nil || s.Run.Status != "worker_died" {
		t.Errorf("after reclaim: %+v", s)
	}
	assertGone(t, sleeper.Process.Pid)
}

// A signal to the worker stops the agent; the task keeps the run's session.
func TestTaskWorkStopped(t *testing.T) {
	q := newQueue(t, "sleep")
	id := q.create("hi")
	s := call{path: q.path, env: []string{"AITS_HOME=" + q.home}, args: []string{"task", "work", "--json"}}.start(t)
	waitFile(t, filepath.Join(q.path, "child.pid"))
	if r := decodeOne[taskRecord](t, q.run("show", id, "--json").stdout); r.Lease == nil || r.Lease.PGID != readPID(t, filepath.Join(q.path, "child.pid")) {
		t.Errorf("running task lease = %+v, want the agent's process group", r.Lease)
	}
	if got := recordKeys(t, q.run("show", id, "--json").stdout, "lease"); !slices.Equal(got, []string{"pgid", "started_at", "token"}) {
		t.Errorf("lease keys = %v", got)
	}
	if out := q.run("list", "--json"); out.code != 0 || decodeOne[taskList](t, out.stdout).Tasks[0].Status != "running" {
		t.Errorf("list while the worker runs: code=%d stdout=%q", out.code, out.stdout)
	}
	s.signal(t, syscall.SIGTERM)
	out := s.waitOrKill(t, 10*time.Second)
	if w := decodeOne[taskRecord](t, out.stdout); out.code != 1 || w.Status != "failed" || w.Run.Status != "stopped" || w.Run.SessionID != "sess-fake-1" {
		t.Fatalf("code=%d %+v", out.code, w)
	}
	// A run whose agent ran keeps every field of the shell result (#57).
	if got := recordKeys(t, out.stdout, "run"); !slices.Equal(got, []string{"agent", "duration_ms", "exit_code", "finished_at", "message", "session_id", "started_at", "status"}) {
		t.Errorf("run keys = %v", got)
	}
}

// A second signal kills a stopping worker's agent group at once (P10).
func TestTaskWorkSecondSignalKills(t *testing.T) {
	q := newQueue(t, "ignore_term")
	q.create("hi")
	s := call{path: q.path, env: []string{"AITS_HOME=" + q.home}, args: []string{"task", "work", "--json"}}.start(t)
	waitFile(t, filepath.Join(q.path, "child.pid"))
	s.signal(t, syscall.SIGINT, syscall.SIGTERM)
	out := s.waitOrKill(t, 3*time.Second)
	if w := decodeOne[taskRecord](t, out.stdout); out.code != 1 || w.Status != "failed" || w.Run.Status != "stopped" {
		t.Fatalf("code=%d %+v", out.code, w)
	}
	assertGone(t, readPID(t, filepath.Join(q.path, "child.pid")))
	assertGone(t, readPID(t, filepath.Join(q.path, "grandchild.pid")))
}

// A worker killed mid-run is found dead by the next list at once, not after
// the task's 30-minute timeout, and its agent, still running, is stopped: the
// agent does not keep the worker's lock (P10).
func TestTaskWorkerKilled(t *testing.T) {
	q := newQueue(t, "sleep")
	id := q.create("hi")
	s := call{path: q.path, env: []string{"AITS_HOME=" + q.home}, args: []string{"task", "work", "--json"}}.start(t)
	waitFile(t, filepath.Join(q.path, "child.pid"))
	pgid := readPID(t, filepath.Join(q.path, "child.pid"))
	q.waitLease(id, pgid)
	if err := s.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	s.waitOrKill(t, 10*time.Second)
	if out := q.run("list"); out.code != 0 {
		t.Fatalf("list: code=%d stderr=%q", out.code, out.stderr)
	}
	show := q.run("show", id, "--json").stdout
	if r := decodeOne[taskRecord](t, show); r.Status != "failed" || r.Run == nil || r.Run.Status != "worker_died" {
		t.Errorf("after the worker was killed: %+v", r)
	}
	// When its agent started is known; its exit code and duration are not
	// (P6, #57).
	if got := recordKeys(t, show, "run"); !slices.Equal(got, []string{"agent", "finished_at", "message", "started_at", "status"}) {
		t.Errorf("run keys = %v", got)
	}
	assertGone(t, pgid)
}

// A lock file removed by hand while its worker runs cannot be judged: list
// leaves the task running and says so, and the worker still records its run
// (P6).
func TestTaskLockFileRemoved(t *testing.T) {
	q := newQueue(t, "sleep")
	id := q.create("hi")
	s := call{path: q.path, env: []string{"AITS_HOME=" + q.home}, args: []string{"task", "work", "--json"}}.start(t)
	waitFile(t, filepath.Join(q.path, "child.pid"))
	q.waitLease(id, readPID(t, filepath.Join(q.path, "child.pid")))
	if err := os.Remove(q.lockFile(id)); err != nil {
		t.Fatal(err)
	}
	want := "aits: cannot tell whether the worker of task " + id + " is alive: its lock file is missing\n"
	for _, args := range [][]string{{"list"}, {"list", "--json"}} {
		if out := q.run(args...); out.code != 0 || out.stderr != want {
			t.Errorf("%v: code=%d stderr=%q, want %q", args, out.code, out.stderr, want)
		}
	}
	if r := decodeOne[taskRecord](t, q.run("show", id, "--json").stdout); r.Status != "running" {
		t.Errorf("task = %+v, want still running", r)
	}
	s.signal(t, syscall.SIGTERM)
	out := s.waitOrKill(t, 10*time.Second)
	if w := decodeOne[taskRecord](t, out.stdout); out.code != 1 || w.Status != "failed" || w.Run == nil || w.Run.Status != "stopped" {
		t.Errorf("work after SIGTERM: code=%d %+v", out.code, w)
	}
}

// work reports a running task whose lock file is missing as list does, and
// still works the queue (#60).
func TestTaskWorkLockFileMissing(t *testing.T) {
	q := newQueue(t, "ok")
	stale := q.create("hi")
	q.setRunning(stale, map[string]any{"token": "w", "pgid": 0})
	want := "aits: cannot tell whether the worker of task " + stale + " is alive: its lock file is missing\n"
	for _, args := range [][]string{{"work"}, {"work", "--json"}} {
		id := q.create("hi")
		out := q.run(args...)
		if out.code != 0 || !strings.HasPrefix(out.stderr, want) {
			t.Errorf("%v: code=%d stderr=%q, want %q first", args, out.code, out.stderr, want)
		}
		if r := decodeOne[taskRecord](t, q.run("show", id, "--json").stdout); r.Status != "done" {
			t.Errorf("%v: task = %+v, want done", args, r)
		}
	}
}

// A worker whose task was reclaimed before it recorded the run fails with
// internal and says so; the reclaim stopped its agent (#60).
func TestTaskWorkReclaimed(t *testing.T) {
	for _, mode := range []string{"text", "json"} {
		t.Run(mode, func(t *testing.T) {
			q := newQueue(t, "sleep")
			id := q.create("hi")
			args := []string{"task", "work"}
			if mode == "json" {
				args = append(args, "--json")
			}
			s := call{path: q.path, env: []string{"AITS_HOME=" + q.home}, args: args}.start(t)
			waitFile(t, filepath.Join(q.path, "child.pid"))
			agent := readPID(t, filepath.Join(q.path, "child.pid"))
			q.waitLease(id, agent)
			// A new lock file is not the one the worker holds, so list takes
			// the worker for dead.
			if err := os.Remove(q.lockFile(id)); err != nil {
				t.Fatal(err)
			}
			q.writeLockFile(id)
			if out := q.run("list"); out.code != 0 {
				t.Fatalf("list: code=%d stderr=%q", out.code, out.stderr)
			}
			out := s.waitOrKill(t, 10*time.Second)
			msg := "task " + id + " was reclaimed before its run was recorded"
			if mode == "text" {
				if out.code != 1 || out.stdout != "" || out.stderr != "aits: "+msg+"\n" {
					t.Errorf("work: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
				}
			} else if e := decode(t, out.stdout).Error; out.code != 1 || e == nil || e.Code != "internal" || e.Message != msg {
				t.Errorf("work: code=%d stdout=%q", out.code, out.stdout)
			}
			if r := decodeOne[taskRecord](t, q.run("show", id, "--json").stdout); r.Status != "failed" || r.Run == nil || r.Run.Status != "worker_died" {
				t.Errorf("task = %+v, want failed by worker_died", r)
			}
			assertGone(t, agent)
			assertGone(t, readPID(t, filepath.Join(q.path, "grandchild.pid")))
		})
	}
}

// A lock that cannot be tried fails list and work with internal and changes
// no task (P6).
func TestTaskLockError(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		q := newQueue(t, "ok")
		id := q.create("hi")
		b := q.setRunning(id, map[string]any{"token": "w", "pgid": 0})
		if err := os.Mkdir(q.lockFile(id), 0o700); err != nil {
			t.Fatal(err)
		}
		out := q.run("list", "--json")
		if e := decode(t, out.stdout).Error; out.code != 1 || e == nil || e.Code != "internal" {
			t.Errorf("list: code=%d stdout=%q", out.code, out.stdout)
		}
		if after, _ := os.ReadFile(q.taskFile(id)); string(after) != string(b) {
			t.Errorf("record changed: %s", after)
		}
	})
	t.Run("queued", func(t *testing.T) {
		q := newQueue(t, "ok")
		id := q.create("hi")
		if err := os.Mkdir(q.lockFile(id), 0o700); err != nil {
			t.Fatal(err)
		}
		out := q.run("work", "--json")
		if e := decode(t, out.stdout).Error; out.code != 1 || e == nil || e.Code != "internal" {
			t.Errorf("work: code=%d stdout=%q", out.code, out.stdout)
		}
		if r := decodeOne[taskRecord](t, q.run("show", id, "--json").stdout); r.Status != "queued" || r.Lease != nil {
			t.Errorf("task changed: %+v", r)
		}
	})
}

// Unreadable task files are skipped and reported by list and work, never
// changed, and the rest of the queue keeps working (P10); show of one says
// why (P6).
func TestTaskUnreadableFiles(t *testing.T) {
	const skip = "aits: skipped task %s that could not be read\n"

	t.Run("garbage", func(t *testing.T) {
		q := newQueue(t, "ok")
		a := q.create("hi")
		file := q.writeTaskFile("20261014T000000-bad000", "garbage", 0o600)
		out := q.run("list", "--json")
		l := decodeOne[taskList](t, out.stdout)
		if out.code != 0 || out.stderr != "" || len(l.Tasks) != 1 || l.Tasks[0].ID != a || !slices.Equal(l.Skipped, []string{"20261014T000000-bad000"}) {
			t.Errorf("list --json: code=%d %+v stderr=%q", out.code, l, out.stderr)
		}
		if out := q.run("list"); out.code != 0 || !strings.HasPrefix(out.stdout, a+"  queued") || strings.Count(out.stdout, "\n") != 1 ||
			out.stderr != fmt.Sprintf(skip, "20261014T000000-bad000") {
			t.Errorf("list: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
		}
		out = q.run("work", "--json")
		if w := decodeOne[taskRecord](t, out.stdout); out.code != 0 || w.ID != a || w.Status != "done" || out.stderr != fmt.Sprintf(skip, "20261014T000000-bad000") {
			t.Errorf("work --json: code=%d %+v stderr=%q", out.code, w, out.stderr)
		}
		if out := q.run("work", "--json"); out.code != 0 || out.stdout != `{"status":"empty"}`+"\n" {
			t.Errorf("work, empty: code=%d stdout=%q", out.code, out.stdout)
		}
		if b, err := os.ReadFile(file); err != nil || string(b) != "garbage" {
			t.Errorf("bad file changed: %q, %v", b, err)
		}
	})

	t.Run("empty and unreadable", func(t *testing.T) {
		q := newQueue(t, "ok")
		q.create("hi")
		q.writeTaskFile("20261014T000000-bad001", "", 0o600)
		want := []string{"20261014T000000-bad001"}
		if os.Geteuid() != 0 {
			q.writeTaskFile("20261014T000000-bad002", "{}", 0o000)
			want = append(want, "20261014T000000-bad002")
		}
		if l := decodeOne[taskList](t, q.run("list", "--json").stdout); !slices.Equal(l.Skipped, want) {
			t.Errorf("list --json skipped %q, want %q", l.Skipped, want)
		}
		var lines string
		for _, id := range want {
			lines += fmt.Sprintf(skip, id)
		}
		for _, args := range [][]string{{"list"}, {"work", "--json"}} {
			if out := q.run(args...); out.stderr != lines {
				t.Errorf("%v: stderr=%q, want %q", args, out.stderr, lines)
			}
		}
	})

	t.Run("newline in the name", func(t *testing.T) {
		q := newQueue(t, "ok")
		q.run("list") // makes the task directory
		q.writeTaskFile("20261014T000000-bad\naits: forged", "garbage", 0o600)
		if out := q.run("list"); out.stderr != fmt.Sprintf(skip, `20261014T000000-bad\naits: forged`) {
			t.Errorf("list: stderr=%q", out.stderr)
		}
	})

	t.Run("empty object", func(t *testing.T) {
		q := newQueue(t, "ok")
		q.run("list") // makes the task directory
		q.writeTaskFile("20261014T000000-bad003", "{}", 0o600)
		if l := decodeOne[taskList](t, q.run("list", "--json").stdout); len(l.Tasks) != 0 || !slices.Equal(l.Skipped, []string{"20261014T000000-bad003"}) {
			t.Errorf("list --json: %+v", l)
		}
	})

	t.Run("copied record", func(t *testing.T) {
		q := newQueue(t, "ok")
		a := q.create("hi")
		b, err := os.ReadFile(filepath.Join(q.home, "task", a+".json"))
		if err != nil {
			t.Fatal(err)
		}
		q.writeTaskFile("20261014T000000-c0ffee", string(b), 0o600)
		if w := decodeOne[taskRecord](t, q.run("work", "--json").stdout); w.ID != a || w.Status != "done" {
			t.Errorf("first work: %+v", w)
		}
		if out := q.run("work", "--json"); out.stdout != `{"status":"empty"}`+"\n" {
			t.Errorf("second work ran the copy: %q", out.stdout)
		}
		l := decodeOne[taskList](t, q.run("list", "--json").stdout)
		if len(l.Tasks) != 1 || l.Tasks[0].ID != a || l.Tasks[0].Status != "done" || !slices.Equal(l.Skipped, []string{"20261014T000000-c0ffee"}) {
			t.Errorf("list --json: %+v", l)
		}
	})

	t.Run("show", func(t *testing.T) {
		q := newQueue(t, "ok")
		q.create("hi") // makes the task directory
		for _, body := range []string{"garbage", "{}"} {
			q.writeTaskFile("20261014T000000-bad004", body, 0o600)
			prefix := "task 20261014T000000-bad004 could not be read: "
			out := q.run("show", "20261014T000000-bad004", "--json")
			if e := decode(t, out.stdout).Error; out.code != 1 || e == nil || e.Code != "internal" || !strings.HasPrefix(e.Message, prefix) {
				t.Errorf("show --json %s: code=%d stdout=%q", body, out.code, out.stdout)
			}
			if out := q.run("show", "20261014T000000-bad004"); out.code != 1 || out.stdout != "" || !strings.HasPrefix(out.stderr, "aits: "+prefix) {
				t.Errorf("show %s: code=%d stdout=%q stderr=%q", body, out.code, out.stdout, out.stderr)
			}
		}
	})
}

// lockDir takes the task directory's lock, as another aits command would.
func (q queue) lockDir() func() {
	q.t.Helper()
	f, err := os.OpenFile(filepath.Join(q.home, "task", ".lock"), os.O_RDWR, 0)
	if err == nil {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	}
	if err != nil {
		q.t.Fatal(err)
	}
	return func() { f.Close() }
}

// deadWorker makes task id running as a dead worker leaves it: unlocked, its
// pgid a leftover group running "sh -c script READY args...". The script sets
// its traps and then creates READY ($0); args are $1 and on. It returns the
// group.
func (q queue) deadWorker(id, script string, args ...string) int {
	q.t.Helper()
	ready := filepath.Join(q.t.TempDir(), "ready")
	g := exec.Command("sh", append([]string{"-c", script, ready}, args...)...)
	g.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := g.Start(); err != nil {
		q.t.Fatal(err)
	}
	go func() { _ = g.Wait() }()
	q.t.Cleanup(func() { _ = syscall.Kill(-g.Process.Pid, syscall.SIGKILL) })
	waitFile(q.t, ready) // a signal before the script's trap would end it at once
	q.setRunning(id, map[string]any{"token": "dead", "pgid": g.Process.Pid})
	q.writeLockFile(id)
	return g.Process.Pid
}

// claimThenHoldDir queues task a (with flags), oldest, and a dead worker's
// task whose leftover group ignores SIGTERM, so that work's claim, after
// marking a running, waits to stop that group before it starts a's agent. It
// starts work, waits for the claim, takes the directory lock, checks that a's
// agent has not started yet, and then kills the group so that the agent
// starts while the test holds the lock.
func claimThenHoldDir(t *testing.T, q queue, flags ...string) (string, *started, func()) {
	t.Helper()
	a := q.create("hi", flags...)
	g := q.deadWorker(q.create("dead"), `trap '' TERM; : > "$0"; exec sleep 60`)
	s := call{path: q.path, env: []string{"AITS_HOME=" + q.home}, args: []string{"task", "work", "--json"}}.start(t)
	q.waitLease(a, 0)
	unlock := q.lockDir()
	if r := decodeOne[taskRecord](t, q.run("show", a, "--json").stdout); r.Lease == nil || r.Lease.PGID != 0 {
		t.Fatalf("the agent started before the test held the directory lock: %+v", r.Lease)
	}
	if err := syscall.Kill(-g, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	return a, s, unlock
}

// A worker killed after its agent started, while another command holds the
// directory lock, has still recorded the agent's group: the next list stops
// it instead of leaving it to run unsupervised (P10, #44).
func TestTaskWorkerKilledBeforePgid(t *testing.T) {
	q := newQueue(t, "sleep")
	a, s, unlock := claimThenHoldDir(t, q)
	waitFile(t, filepath.Join(q.path, "child.pid"))
	agent := readPID(t, filepath.Join(q.path, "child.pid"))
	q.waitLease(a, agent) // recorded while the directory lock is held
	if err := s.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	s.waitOrKill(t, 10*time.Second)
	unlock()
	if out := q.run("list"); out.code != 0 {
		t.Fatalf("list: code=%d stderr=%q", out.code, out.stderr)
	}
	if r := decodeOne[taskRecord](t, q.run("show", a, "--json").stdout); r.Status != "failed" || r.Run == nil || r.Run.Status != "worker_died" {
		t.Errorf("after the worker was killed: %+v", r)
	}
	assertGone(t, agent)
	assertGone(t, readPID(t, filepath.Join(q.path, "grandchild.pid")))
}

// A worker that cannot record its agent's group kills the agent at once,
// grace or not, instead of leaving it unrecorded: had the worker died, no
// later command could have stopped it (P10, #56).
func TestTaskWorkPgidNotRecorded(t *testing.T) {
	q := newQueue(t, "ignore_term")
	t.Cleanup(func() { assertNoneRunning(t, q.path) }) // nothing from the agent survives, also when work hangs
	a := q.create("hi")
	dir := filepath.Join(q.home, "task")
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	// Stopped by work's claim once the claim is written, the leftover group
	// makes the task directory read-only.
	q.deadWorker(q.create("dead"), `trap 'chmod 500 "$1"; exit 0' TERM; : > "$0"; sleep 60 & wait`, dir)

	s := call{path: q.path, env: []string{"AITS_HOME=" + q.home}, args: []string{"task", "work", "--json"}}.start(t)
	out := s.waitOrKill(t, 3*time.Second) // under the 5 s grace
	if e := decode(t, out.stdout).Error; out.code != 1 || e == nil || e.Code != "internal" || !strings.Contains(e.Message, a) {
		t.Errorf("work: code=%d stdout=%q", out.code, out.stdout)
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if out := q.run("list"); out.code != 0 {
		t.Fatalf("list: code=%d stderr=%q", out.code, out.stderr)
	}
	if r := decodeOne[taskRecord](t, q.run("show", a, "--json").stdout); r.Status != "failed" || r.Run == nil || r.Run.Status != "worker_died" {
		t.Errorf("after the worker exited: %+v", r)
	}
}

// A run's timeout counts from its agent's start, however long another command
// holds the directory lock (#44).
func TestTaskTimeoutWhileDirLocked(t *testing.T) {
	q := newQueue(t, "sleep")
	_, s, unlock := claimThenHoldDir(t, q, "--timeout", "1s")
	waitFile(t, filepath.Join(q.path, "child.pid"))
	assertGone(t, readPID(t, filepath.Join(q.path, "child.pid")))
	assertGone(t, readPID(t, filepath.Join(q.path, "grandchild.pid")))
	unlock()
	out := s.waitOrKill(t, 15*time.Second)
	if w := decodeOne[taskRecord](t, out.stdout); out.code != 1 || w.Run == nil || w.Run.Status != "timeout" {
		t.Errorf("work: code=%d %+v", out.code, w)
	}
}

// task create reads "-" through the same bound: over 16 MiB is invalid input
// and nothing is queued (#7).
func TestTaskCreatePromptFromStdinBound(t *testing.T) {
	q := newQueue(t, "ok")
	out := call{path: q.path, env: []string{"AITS_HOME=" + q.home}, stdin: strings.Repeat("p", 16<<20+1),
		args: []string{"task", "create", "--agent", "claude", "--cwd", t.TempDir(), "--json", "-"}}.run(t)
	if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "invalid_input" {
		t.Errorf("create: code=%d stdout=%q", out.code, out.stdout)
	}
	if q.queuedAny() {
		t.Errorf("an oversized prompt was queued")
	}
}

// A follow-up runs in its parent's session, with its agent and cwd, given
// neither (#63). The parent's session id is patched to a unique value: the
// fake always reports sess-fake-1.
func TestTaskFollowUpResumesParentSession(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		q := queue{t: t, home: t.TempDir(), path: vendorAs(t, agent, "ok")}
		p := q.create("review", "--agent", agent)
		if out := q.run("work"); out.code != 0 {
			t.Fatalf("work: code=%d stderr=%q", out.code, out.stderr)
		}
		parent := decodeOne[taskRecord](t, q.run("show", p, "--json").stdout)
		q.patch(p, map[string]any{"run": map[string]any{"status": "ok", "agent": agent, "session_id": "sess-parent-7", "answer": "a", "finished_at": parent.Run.FinishedAt}})
		f := q.after(p, "fix it")
		w := decodeOne[taskRecord](t, q.run("work", "--json").stdout)
		if w.ID != f || w.Status != "done" || w.After != p || w.Agent != agent || w.Cwd != parent.Cwd {
			t.Errorf("follow-up = %+v", w)
		}
		l := decodeOne[taskList](t, q.run("list", "--json").stdout)
		if i := slices.IndexFunc(l.Tasks, func(e taskListItem) bool { return e.ID == f }); i < 0 || l.Tasks[i].After != p {
			t.Errorf("list = %+v, want %s after %s", l.Tasks, f, p)
		}
		rec := readRecord(t, q.path)
		want := map[string][]string{"claude": {"--resume", "sess-parent-7"}, "codex": {"resume", "sess-parent-7"}}[agent]
		if i := slices.Index(rec.Args, want[0]); i < 0 || i+1 >= len(rec.Args) || rec.Args[i+1] != want[1] {
			t.Errorf("agent argv = %q, want %q", rec.Args, want)
		}
	})
}

// While a worker runs the parent, a second worker passes over its follow-up
// and claims a newer task; the follow-up stays queued. When the parent then
// fails, list fails the follow-up (#63).
func TestTaskFollowUpWaitsForParent(t *testing.T) {
	q := newQueue(t, "sleep")
	p := q.create("parent")
	f := q.after(p, "follow")
	x := q.create("other")
	s := call{path: q.path, env: []string{"AITS_HOME=" + q.home}, args: []string{"task", "work", "--json"}}.start(t)
	waitFile(t, filepath.Join(q.path, "child.pid"))
	q.waitLease(p, readPID(t, filepath.Join(q.path, "child.pid")))
	other := call{path: vendor(t, "ok"), env: []string{"AITS_HOME=" + q.home}, args: []string{"task", "work", "--json"}}.run(t)
	if w := decodeOne[taskRecord](t, other.stdout); w.ID != x {
		t.Errorf("second worker claimed %s, want %s (not the follow-up %s)", w.ID, x, f)
	}
	if r := decodeOne[taskRecord](t, q.run("show", f, "--json").stdout); r.Status != "queued" {
		t.Errorf("follow-up = %+v, want queued", r)
	}
	s.signal(t, syscall.SIGTERM)
	s.waitOrKill(t, 10*time.Second)
	q.run("list")
	if r := decodeOne[taskRecord](t, q.run("show", f, "--json").stdout); r.Status != "failed" || r.Run == nil ||
		r.Run.Status != "not_started" || r.Run.Message != "its parent task "+p+" failed" {
		t.Errorf("follow-up of a failed parent = %+v", r)
	}
}

// create --after refuses what it cannot run as asked, and writes nothing
// (#63, spec §4).
func TestTaskCreateAfterInvalid(t *testing.T) {
	q := newQueue(t, "ok")
	p := q.create("parent")
	parent := decodeOne[taskRecord](t, q.run("show", p, "--json").stdout)
	first := q.after(p, "first")
	failed, nosession := q.create("failed"), q.create("no session")
	q.patch(failed, map[string]any{"status": "failed", "run": map[string]any{"status": "timeout", "agent": "claude", "finished_at": parent.CreatedAt}})
	q.patch(nosession, map[string]any{"status": "done", "run": map[string]any{"status": "ok", "agent": "claude", "finished_at": parent.CreatedAt}})
	other := t.TempDir()
	const only = "; only a done task with a session can be followed up"
	for _, tt := range []struct {
		name string
		args []string
		msg  string
	}{
		{"not a task id", []string{"--after", "x"}, "no task x"},
		{"empty after", []string{"--after", ""}, `invalid value "" for flag -after: needs a task id`},
		{"failed parent", []string{"--after", failed}, "task " + failed + " failed" + only},
		{"parent without a session", []string{"--after", nosession}, "task " + nosession + " has no session" + only},
		{"other agent", []string{"--after", p, "--agent", "codex"}, "agent codex is not task " + p + "'s agent claude"},
		{"other cwd", []string{"--after", p, "--cwd", other}, fmt.Sprintf("working directory %q is not task %s's %q", other, p, parent.Cwd)},
		{"second follow-up", []string{"--after", p}, "task " + p + " already has a follow-up: " + first + "; follow up on " + first + " instead"},
	} {
		before := q.count()
		out := q.run(append(append([]string{"create"}, tt.args...), "--json", "x")...)
		if e := decode(t, out.stdout).Error; out.code != 2 || e == nil || e.Code != "invalid_input" || e.Message != tt.msg || q.count() != before {
			t.Errorf("%s: code=%d stdout=%q; want %q", tt.name, out.code, out.stdout, tt.msg)
		}
	}
	// Once the first follow-up has failed, the parent can be followed up again;
	// the same agent and cwd, spelled with a trailing slash, are the parent's.
	q.patch(first, map[string]any{"status": "failed", "run": map[string]any{"status": "not_started", "agent": "claude", "finished_at": parent.CreatedAt}})
	q.after(p, "again", "--cwd", parent.Cwd+"/", "--agent", "claude")
}
