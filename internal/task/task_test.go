package task

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-in-the-shell/aits/internal/shell"
	"github.com/agent-in-the-shell/aits/internal/store"
)

// env is a fresh environment with no agent on its PATH.
func env(t *testing.T) []string {
	return []string{"HOME=" + t.TempDir(), "AITS_HOME=" + t.TempDir(), "PATH=" + t.TempDir()}
}

func create(t *testing.T, environ []string, agent string) Task {
	t.Helper()
	tk, err := Create(environ, Spec{Agent: agent, Cwd: t.TempDir(), Prompt: "hi", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestCreateValidates(t *testing.T) {
	environ := env(t)
	dir := t.TempDir()
	for name, s := range map[string]Spec{
		"no agent":      {Cwd: dir, Prompt: "hi", Timeout: time.Minute},
		"unknown agent": {Agent: "nope", Cwd: dir, Prompt: "hi", Timeout: time.Minute},
		"blank prompt":  {Agent: "claude", Cwd: dir, Prompt: " ", Timeout: time.Minute},
		"relative cwd":  {Agent: "claude", Cwd: "rel", Prompt: "hi", Timeout: time.Minute},
		"missing cwd":   {Agent: "claude", Cwd: filepath.Join(dir, "x"), Prompt: "hi", Timeout: time.Minute},
		"zero timeout":  {Agent: "claude", Cwd: dir, Prompt: "hi"},
		"timeout < 1ms": {Agent: "claude", Cwd: dir, Prompt: "hi", Timeout: time.Microsecond},
		"bad env name":  {Agent: "claude", Cwd: dir, Prompt: "hi", Timeout: time.Minute, Env: []string{"A=B"}},
	} {
		var invalid *shell.InvalidRequestError
		if _, err := Create(environ, s); !errors.As(err, &invalid) {
			t.Errorf("%s: err = %v, want *shell.InvalidRequestError", name, err)
		}
	}
}

// Work claims the oldest queued task, of the given agent if one is named;
// with nothing to run it returns nil. The fake PATH has no agent, so each
// run ends not_started — enough to see which task was claimed.
func TestWorkClaimOrder(t *testing.T) {
	environ := env(t)
	a := create(t, environ, "claude")
	b := create(t, environ, "codex")
	got, _, err := Work(context.Background(), environ, "codex")
	if err != nil || got == nil || got.ID != b.ID {
		t.Fatalf("Work(codex) = %+v, %v; want %s", got, err, b.ID)
	}
	if got, _, _ := Work(context.Background(), environ, ""); got == nil || got.ID != a.ID {
		t.Fatalf("Work() claimed %+v, want %s", got, a.ID)
	}
	if got, _, err := Work(context.Background(), environ, ""); got != nil || err != nil {
		t.Fatalf("Work on an empty queue = %+v, %v", got, err)
	}
}

func TestWorkNotStarted(t *testing.T) {
	environ := env(t)
	tk, err := Create(environ, Spec{Agent: "claude", Cwd: t.TempDir(), Prompt: "hi", Timeout: time.Minute, Env: []string{"AITS_TEST_UNSET"}})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := Work(context.Background(), environ, "")
	if err != nil || got.ID != tk.ID || got.Status != Failed || got.Run == nil || got.Run.Status != NotStarted || got.Run.Message == "" || !got.Run.StartedAt.IsZero() || got.Lease != nil {
		t.Fatalf("Work = %+v, %v", got, err)
	}
}

func TestReclaim(t *testing.T) {
	environ := env(t)
	expired := create(t, environ, "claude")
	live := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	for _, x := range []struct {
		tk      Task
		expires time.Time
	}{{expired, time.Now().Add(-time.Second)}, {live, time.Now().Add(time.Hour)}} {
		x.tk.Status, x.tk.Lease = Running, &Lease{Token: "w", WorkerPID: deadPID(t), ExpiresAt: x.expires}
		if err := write(dir, x.tk); err != nil {
			t.Fatal(err)
		}
	}
	tasks, _, err := List(environ)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("List = %+v, %v", tasks, err)
	}
	if e := tasks[0]; e.Status != Failed || e.Run == nil || e.Run.Status != LeaseExpired || e.Lease != nil {
		t.Errorf("expired task = %+v", e)
	}
	if l := tasks[1]; l.Status != Running || l.Lease == nil {
		t.Errorf("live task = %+v", l)
	}
}

// A worker whose lease was reclaimed must not overwrite the failed record.
func TestUpdateForeignToken(t *testing.T) {
	environ := env(t)
	tk := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	tk.Status, tk.Lease = Running, &Lease{Token: "other"}
	if err := write(dir, tk); err != nil {
		t.Fatal(err)
	}
	if _, err := update(dir, tk.ID, "mine", func(t *Task) { t.Status = Done }); !errors.Is(err, errReclaimed) {
		t.Fatalf("update = %v, want errReclaimed", err)
	}
	if got, _ := Show(environ, tk.ID); got.Status != Running {
		t.Errorf("record overwritten: %+v", got)
	}
}

func TestShowUnknown(t *testing.T) {
	var invalid *shell.InvalidRequestError
	for _, id := range []string{"nope", "../../etc/passwd"} {
		if _, err := Show(env(t), id); !errors.As(err, &invalid) {
			t.Errorf("Show(%q) err = %v", id, err)
		}
	}
}

// A lease that has expired by the wall clock while its worker is still alive
// (the laptop slept: shell's timeout uses the monotonic clock) is left alone.
func TestReclaimSparesLiveWorker(t *testing.T) {
	environ := env(t)
	tk := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	tk.Status, tk.Lease = Running, &Lease{Token: "w", WorkerPID: os.Getpid(), ExpiresAt: time.Now().Add(-time.Hour)}
	if err := write(dir, tk); err != nil {
		t.Fatal(err)
	}
	if tasks, _, err := List(environ); err != nil || tasks[0].Status != Running {
		t.Fatalf("List = %+v, %v; want the task still running", tasks, err)
	}
}

// A signal that arrives while work waits for the lock touches no task.
func TestWorkStoppedBeforeClaim(t *testing.T) {
	environ := env(t)
	tk := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	unlock, err := store.Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, _, err := Work(ctx, environ, ""); done <- err }()
	time.Sleep(100 * time.Millisecond) // let Work block on the lock
	cancel()
	unlock()
	if err := <-done; !errors.Is(err, shell.ErrStopped) {
		t.Fatalf("Work err = %v, want shell.ErrStopped", err)
	}
	if got, _ := Show(environ, tk.ID); got.Status != Queued || got.Run != nil {
		t.Errorf("task touched: %+v", got)
	}
}

// deadPID returns the pid of a process that has exited: a worker that died.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$") // this test binary
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// The record keeps --env names; values stay in the worker's environment (P8).
func TestEnvValuesNotStored(t *testing.T) {
	environ := append(env(t), "AITS_TEST_SECRET=s3cr3t-value")
	tk, err := Create(environ, Spec{Agent: "claude", Cwd: t.TempDir(), Prompt: "hi", Timeout: time.Minute, Env: []string{"AITS_TEST_SECRET"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Work(context.Background(), environ, ""); err != nil {
		t.Fatal(err)
	}
	dir, err := store.Dir(environ, "task")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, tk.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if s := string(data); !strings.Contains(s, "AITS_TEST_SECRET") || strings.Contains(s, "s3cr3t-value") {
		t.Fatalf("task file = %s", s)
	}
}

// A task file is readable only as a record of its own id with a known
// status, and a running record has its lease (P6, P10).
func TestReadRejects(t *testing.T) {
	environ := env(t)
	tk := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	valid, err := os.ReadFile(path(dir, tk.ID))
	if err != nil {
		t.Fatal(err)
	}
	other := strings.Replace(string(valid), tk.ID, "20000101T000000-000000", 1)
	for name, body := range map[string]string{
		"garbage":           "garbage",
		"empty":             "",
		"empty object":      "{}",
		"another id":        other,
		"unknown status":    strings.Replace(string(valid), `"status":"queued"`, `"status":"paused"`, 1),
		"running, no lease": strings.Replace(string(valid), `"status":"queued"`, `"status":"running"`, 1),
	} {
		file := filepath.Join(t.TempDir(), tk.ID+".json")
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := read(file); err == nil {
			t.Errorf("%s: read = %+v, want an error", name, got)
		}
	}
	if got, err := read(path(dir, tk.ID)); err != nil || got.ID != tk.ID {
		t.Errorf("valid record: %+v, %v", got, err)
	}
}

// List and Work skip unreadable task files, report their ids and leave them
// as they are; the rest of the queue keeps working (P10).
func TestUnreadableSkipped(t *testing.T) {
	environ := env(t)
	tk := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	bad := filepath.Join(dir, "20000101T000000-bad000.json")
	if err := os.WriteFile(bad, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	tasks, skipped, err := List(environ)
	if err != nil || len(tasks) != 1 || tasks[0].ID != tk.ID || !slices.Equal(skipped, []string{"20000101T000000-bad000"}) {
		t.Fatalf("List = %+v, %q, %v", tasks, skipped, err)
	}
	got, skipped, err := Work(context.Background(), environ, "")
	if err != nil || got == nil || got.ID != tk.ID || !slices.Equal(skipped, []string{"20000101T000000-bad000"}) {
		t.Fatalf("Work = %+v, %q, %v", got, skipped, err)
	}
	if b, err := os.ReadFile(bad); err != nil || string(b) != "garbage" {
		t.Errorf("bad file changed: %q, %v", b, err)
	}
}

// A task directory that cannot be listed still fails.
func TestLoadDirError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(file); err == nil {
		t.Fatal("load of a regular file succeeded")
	}
}
