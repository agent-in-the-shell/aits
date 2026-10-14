package main

import (
	"encoding/json"
	"fmt"
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

// taskList is the --json output of task list; decoding rejects any field not
// listed here (P5).
type taskList struct {
	Tasks []struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		Agent     string `json:"agent"`
		Cwd       string `json:"cwd"`
		CreatedAt string `json:"created_at"`
	} `json:"tasks"`
	Skipped []string `json:"skipped"`
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
	file := filepath.Join(q.home, "task", id+".json")
	if err := os.WriteFile(file, []byte(body), mode); err != nil {
		q.t.Fatal(err)
	}
	return file
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
	// An unreadable file next to it does not stop the reclaim.
	q.writeTaskFile("20261014T000000-bad000", "garbage", 0o600)
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
