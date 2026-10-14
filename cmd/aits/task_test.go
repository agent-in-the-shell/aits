package main

import (
	"encoding/json"
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
	CreatedAt string   `json:"created_at"`
	Lease     *struct {
		Token     string `json:"token"`
		WorkerPID int    `json:"worker_pid"`
		PGID      int    `json:"pgid"`
		ExpiresAt string `json:"expires_at"`
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

// queue is a shared AITS_HOME and fake-agent PATH for one test.
type queue struct {
	t          *testing.T
	home, path string
}

func newQueue(t *testing.T, scenario string) queue {
	return queue{t: t, home: t.TempDir(), path: vendor(t, scenario)}
}

func (q queue) run(args ...string) output {
	q.t.Helper()
	return call{path: q.path, env: []string{"AITS_HOME=" + q.home}, args: append([]string{"task"}, args...)}.run(q.t)
}

func (q queue) create(prompt string, flags ...string) string {
	q.t.Helper()
	out := q.run(append(append([]string{"create", "--agent", "claude", "--cwd", q.t.TempDir(), "--json"}, flags...), prompt)...)
	var r struct{ ID, Status string }
	if err := json.Unmarshal([]byte(out.stdout), &r); out.code != 0 || err != nil || r.Status != "queued" {
		q.t.Fatalf("create: code=%d stdout=%q", out.code, out.stdout)
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
	if entries, _ := os.ReadDir(filepath.Join(q.home, "task")); slices.ContainsFunc(entries, func(e os.DirEntry) bool { return strings.HasSuffix(e.Name(), ".json") }) {
		t.Errorf("invalid input wrote a task")
	}
}

// A run whose agent never started has no started_at (help, spec §4).
func TestTaskNotStartedHasNoStartedAt(t *testing.T) {
	q := newQueue(t, "ok")
	q.create("hi", "--env", "AITS_TEST_UNSET")
	out := q.run("work", "--json")
	if w := decodeOne[taskRecord](t, out.stdout); out.code != 1 || w.Run == nil || w.Run.Status != "not_started" || w.Run.StartedAt != "" {
		t.Fatalf("work: code=%d %+v", out.code, w)
	}
}

func TestTaskWorkFailed(t *testing.T) {
	q := newQueue(t, "exit_nonzero")
	id := q.create("hi")
	out := q.run("work", "--json")
	if w := decodeOne[taskRecord](t, out.stdout); out.code != 1 || w.ID != id || w.Status != "failed" || w.Run.Status != "agent_failed" {
		t.Fatalf("code=%d %+v", out.code, w)
	}
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

// A running task whose lease has expired is failed by the next list, and its
// leftover agent group is stopped (P10).
func TestTaskReclaim(t *testing.T) {
	q := newQueue(t, "ok")
	id := q.create("hi")
	sleeper := exec.Command("sleep", "60")
	sleeper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = sleeper.Wait() }()
	file := filepath.Join(q.home, "task", id+".json")
	var rec map[string]any
	b, _ := os.ReadFile(file)
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	rec["status"] = "running"
	worker := exec.Command("true") // a worker that has died
	if err := worker.Run(); err != nil {
		t.Fatal(err)
	}
	rec["lease"] = map[string]any{"token": "dead", "worker_pid": worker.Process.Pid, "pgid": sleeper.Process.Pid,
		"expires_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}
	b, _ = json.Marshal(rec)
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if out := q.run("list"); out.code != 0 {
		t.Fatalf("list: code=%d stderr=%q", out.code, out.stderr)
	}
	if s := decodeOne[taskRecord](t, q.run("show", id, "--json").stdout); s.Status != "failed" || s.Run == nil || s.Run.Status != "lease_expired" {
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
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	out := s.waitOrKill(t, 10*time.Second)
	if w := decodeOne[taskRecord](t, out.stdout); out.code != 1 || w.Status != "failed" || w.Run.Status != "stopped" || w.Run.SessionID != "sess-fake-1" {
		t.Fatalf("code=%d %+v", out.code, w)
	}
}
