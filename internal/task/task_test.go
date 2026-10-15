package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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
	got, _, err := Work(context.Background(), nil, environ, "codex")
	if err != nil || got == nil || got.ID != b.ID {
		t.Fatalf("Work(codex) = %+v, %v; want %s", got, err, b.ID)
	}
	if got, _, _ := Work(context.Background(), nil, environ, ""); got == nil || got.ID != a.ID {
		t.Fatalf("Work() claimed %+v, want %s", got, a.ID)
	}
	if got, _, err := Work(context.Background(), nil, environ, ""); got != nil || err != nil {
		t.Fatalf("Work on an empty queue = %+v, %v", got, err)
	}
}

func TestWorkNotStarted(t *testing.T) {
	environ := env(t)
	tk, err := Create(environ, Spec{Agent: "claude", Cwd: t.TempDir(), Prompt: "hi", Timeout: time.Minute, Env: []string{"AITS_TEST_UNSET"}})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := Work(context.Background(), nil, environ, "")
	if err != nil || got.ID != tk.ID || got.Status != Failed || got.Run == nil || got.Run.Status != NotStarted || got.Run.Message == "" || !got.Run.StartedAt.IsZero() || got.Lease != nil {
		t.Fatalf("Work = %+v, %v", got, err)
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
	go func() { _, _, err := Work(ctx, nil, environ, ""); done <- err }()
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

// The record keeps --env names; values stay in the worker's environment (P8).
func TestEnvValuesNotStored(t *testing.T) {
	environ := append(env(t), "AITS_TEST_SECRET=s3cr3t-value")
	tk, err := Create(environ, Spec{Agent: "claude", Cwd: t.TempDir(), Prompt: "hi", Timeout: time.Minute, Env: []string{"AITS_TEST_SECRET"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Work(context.Background(), nil, environ, ""); err != nil {
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
		other := t.TempDir()
		if err := os.WriteFile(path(other, tk.ID), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := read(other, tk.ID); err == nil {
			t.Errorf("%s: read = %+v, want an error", name, got)
		}
	}
	link := t.TempDir()
	if err := os.Symlink(path(dir, tk.ID), path(link, tk.ID)); err != nil {
		t.Fatal(err)
	}
	if got, err := read(link, tk.ID); err == nil {
		t.Errorf("symlink to a valid record: read = %+v, want an error", got)
	}
	if got, err := read(dir, tk.ID); err != nil || got.ID != tk.ID {
		t.Errorf("valid record: %+v, %v", got, err)
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

// A file that is not a regular file, or not named by a task id, is not a
// readable task, and reading it never blocks (a FIFO would, P10).
func TestReadRejectsOddFiles(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "20261014T000000-f1f000.json")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := read(dir, "20261014T000000-f1f000"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("read of a FIFO succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read of a FIFO blocked")
	}
	odd := filepath.Join(dir, "foo.json")
	if err := os.WriteFile(odd, []byte(`{"id":"foo","status":"queued"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := read(dir, "foo"); err == nil {
		t.Errorf("read of %s = %+v, want an error", odd, got)
	}
}

// Show takes only task ids: another spelling of one (a capital hex digit,
// which a case-insensitive file system would open), an empty id or a dot
// file is no task.
func TestShowMalformedID(t *testing.T) {
	environ := env(t)
	tk := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	tk.ID = "20261014T000000-abcdef" // hex letters, so the upper-case spelling differs
	if err := write(dir, tk); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".x.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var invalid *shell.InvalidRequestError
	for _, id := range []string{strings.ToUpper(tk.ID), "", ".x"} {
		if got, err := Show(environ, id); !errors.As(err, &invalid) {
			t.Errorf("Show(%q) = %+v, %v; want no task", id, got, err)
		}
	}
}

// A task file that exists but cannot be opened, such as a dangling symlink,
// is unreadable, as list reports it, not missing.
func TestShowDanglingSymlink(t *testing.T) {
	environ := env(t)
	dir, _ := store.Dir(environ, "task")
	id := "20261014T000000-da0000"
	if err := os.Symlink(filepath.Join(dir, "missing.json"), path(dir, id)); err != nil {
		t.Fatal(err)
	}
	var invalid *shell.InvalidRequestError
	if _, err := Show(environ, id); err == nil || errors.As(err, &invalid) || !strings.Contains(err.Error(), "could not be read") {
		t.Errorf("Show = %v, want could not be read", err)
	}
}

// lock takes the lock file of task id as a worker does and returns its
// unlock function; lock(…)() leaves the file as a claim does, unlocked.
func lock(t *testing.T, dir, id string) func() {
	t.Helper()
	unlock, err := store.TryLock(lockPath(dir, id), true)
	if err != nil {
		t.Fatal(err)
	}
	return unlock
}

// running writes tk as a running task with the raw lease JSON, as a worker
// (or an earlier aits) would have left it.
func running(t *testing.T, dir string, tk Task, lease string) {
	t.Helper()
	tk.Status = Running
	b, err := json.Marshal(tk)
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.Replace(b, []byte(`"lease":null`), []byte(`"lease":`+lease), 1)
	if err := store.WriteFile(path(dir, tk.ID), b); err != nil {
		t.Fatal(err)
	}
}

// A running task whose lock nobody holds is failed by the next list, even
// when its recorded worker pid is alive and its old lease has not expired:
// liveness is the lock, not the pid or the clock (P10). Its pgid is 0, as
// for a worker that died before the agent started: nothing is signalled.
func TestReclaimDeadWorker(t *testing.T) {
	environ := env(t)
	tk := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	running(t, dir, tk, fmt.Sprintf(`{"token":"w","worker_pid":%d,"pgid":0,"expires_at":%q}`,
		os.Getpid(), time.Now().Add(time.Hour).UTC().Format(time.RFC3339)))
	lock(t, dir, tk.ID)() // as a claim leaves it
	tasks, _, err := List(environ)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("List = %+v, %v", tasks, err)
	}
	if e := tasks[0]; e.Status != Failed || e.Run == nil || e.Run.Status != "worker_died" ||
		e.Run.Message != "the worker exited before it recorded a result" || e.Lease != nil {
		t.Errorf("dead worker's task = %+v", e)
	}
}

// A live worker keeps its task, however old: it holds the lock (here the
// test does). Once the lock is free, the next list fails the task.
func TestReclaimSparesLiveWorker(t *testing.T) {
	environ := env(t)
	tk := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	running(t, dir, tk, `{"token":"w","pgid":0}`)
	unlock := lock(t, dir, tk.ID)
	if tasks, _, err := List(environ); err != nil || tasks[0].Status != Running {
		t.Fatalf("List = %+v, %v; want the task still running", tasks, err)
	}
	unlock()
	if tasks, _, err := List(environ); err != nil || tasks[0].Status != Failed || tasks[0].Run.Status != "worker_died" {
		t.Fatalf("List after unlock = %+v, %v; want worker_died", tasks, err)
	}
}

// A running task without a lock file, as one left by an earlier aits or after
// the file was removed by hand, cannot be judged: it stays running and is
// reported, and no lock file is made for it (P6).
func TestReclaimMissingLockFile(t *testing.T) {
	environ := env(t)
	tk := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	running(t, dir, tk, fmt.Sprintf(`{"token":"w","worker_pid":1,"pgid":0,"expires_at":%q}`,
		time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)))
	tasks, notes, err := List(environ)
	if err != nil || tasks[0].Status != Running || !slices.Equal(notes.Unknown, []string{tk.ID}) {
		t.Fatalf("List = %+v, %+v, %v; want running and unknown", tasks, notes, err)
	}
	if _, err := os.Stat(lockPath(dir, tk.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("reclaim made a lock file: %v", err)
	}
}

// A lock that cannot be tried fails list and changes no task (P6).
func TestReclaimLockError(t *testing.T) {
	environ := env(t)
	tk := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	running(t, dir, tk, `{"token":"w","pgid":0}`)
	if err := os.Mkdir(lockPath(dir, tk.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := List(environ); err == nil {
		t.Fatal("List succeeded with a lock file that cannot be opened")
	}
	if got, _ := Show(environ, tk.ID); got.Status != Running || got.Lease == nil {
		t.Errorf("task changed: %+v", got)
	}
}

// A queued task whose lock is held (re-queued by hand while its old worker
// runs) is not claimed: work fails and writes nothing.
func TestClaimLockHeld(t *testing.T) {
	environ := env(t)
	tk := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	unlock := lock(t, dir, tk.ID)
	defer unlock()
	if got, _, err := Work(context.Background(), nil, environ, ""); err == nil {
		t.Fatalf("Work = %+v, want an error", got)
	}
	if got, _ := Show(environ, tk.ID); got.Status != Queued || got.Lease != nil {
		t.Errorf("task changed: %+v", got)
	}
}

// A lock file replaced by a symlink to another task's free lock must not be
// taken for a dead worker: that would stop a live agent. It is a lock error,
// and list fails without changing the task (P6).
func TestReclaimLockSymlink(t *testing.T) {
	environ := env(t)
	a := create(t, environ, "claude")
	b := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	running(t, dir, a, `{"token":"w","pgid":0}`)
	lock(t, dir, b.ID)()
	if err := os.Symlink(lockPath(dir, b.ID), lockPath(dir, a.ID)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := List(environ); err == nil {
		t.Error("List took a symlinked lock file")
	}
	if got, _ := Show(environ, a.ID); got.Status != Running || got.Lease == nil {
		t.Errorf("task changed: %+v", got)
	}
}

// A worker writes its pgid without waiting for the directory lock: while
// another command holds it, a killed worker would otherwise leave its agent
// unrecorded (P10). (Its token check is update's, see TestUpdateForeignToken.)
func TestModifyWithoutDirLock(t *testing.T) {
	environ := env(t)
	tk := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	tk.Status, tk.Lease = Running, &Lease{Token: "w"}
	if err := write(dir, tk); err != nil {
		t.Fatal(err)
	}
	unlock, err := store.Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	done := make(chan error, 1)
	go func() {
		_, err := modify(dir, tk.ID, "w", func(t *Task) { t.Lease.PGID = 4242 })
		done <- err
	}()
	select {
	case err := <-done:
		got, rerr := read(dir, tk.ID)
		tk.Lease.PGID = 4242
		if err != nil || rerr != nil || *got.Lease != *tk.Lease || got.Status != Running || !got.CreatedAt.Equal(tk.CreatedAt) {
			t.Errorf("modify = %v; record %+v, %v", err, got, rerr)
		}
	case <-time.After(time.Second):
		t.Fatal("modify waited for the directory lock")
	}
}

// follow creates a follow-up of parent with s's other fields.
func follow(environ []string, parent string, s Spec) (Task, error) {
	s.After, s.Prompt, s.Timeout = parent, "next", time.Minute
	return Create(environ, s)
}

// setStatus rewrites task tk with status st and run r, as a worker would.
func setStatus(t *testing.T, environ []string, tk Task, st Status, r *Run) {
	t.Helper()
	dir, _ := store.Dir(environ, "task")
	tk.Status, tk.Run = st, r
	if err := write(dir, tk); err != nil {
		t.Fatal(err)
	}
}

// An unreadable task file could be a follow-up of the same parent: create
// --after fails closed, and a plain create is unaffected (spec §2, P6).
func TestCreateFollowUpUnreadableFile(t *testing.T) {
	environ := env(t)
	p := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	if err := store.WriteFile(path(dir, "20000101T000000-00000a"), []byte("not json")); err != nil {
		t.Fatal(err)
	}
	var invalid *shell.InvalidRequestError
	if _, err := follow(environ, p.ID, Spec{}); err == nil || errors.As(err, &invalid) ||
		!strings.Contains(err.Error(), "cannot tell whether task "+p.ID+" already has a follow-up") {
		t.Errorf("follow-up with an unreadable file: err = %v", err)
	}
	create(t, environ, "claude")
}

// A follow-up is passed over until its parent is done, then run in the
// parent's session. The fake PATH has no agent, so the run is not_started;
// which task was claimed, and with which session, is what matters.
func TestClaimFollowUp(t *testing.T) {
	environ := env(t)
	p := create(t, environ, "claude")
	f, err := follow(environ, p.ID, Spec{})
	if err != nil {
		t.Fatal(err)
	}
	x := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	running(t, dir, p, `{"token":"w","pgid":0}`)
	unlock := lock(t, dir, p.ID) // a live worker
	got, session, release, _, err := claim(context.Background(), dir, "")
	if err != nil || got == nil || got.ID != x.ID || session != "" {
		t.Fatalf("claim with the parent running = %+v %q, %v; want %s", got, session, err, x.ID)
	}
	release()
	unlock()
	setStatus(t, environ, p, Done, &Run{Result: shell.Result{Status: shell.StatusOK, SessionID: "sess-p"}})
	got, session, release, _, err = claim(context.Background(), dir, "")
	if err != nil || got == nil || got.ID != f.ID || session != "sess-p" {
		t.Fatalf("claim with the parent done = %+v %q, %v; want %s in sess-p", got, session, err, f.ID)
	}
	release()
}

// A queued follow-up whose parent failed, is gone, or is done without a
// session is failed by the next reclaim, down the whole chain, whatever the
// order of created_at (spec §2, §4).
func TestReclaimFailsOrphans(t *testing.T) {
	tests := []struct {
		name   string
		parent func(t *testing.T, environ []string, p Task)
		why    string
	}{
		{"failed", func(t *testing.T, environ []string, p Task) {
			setStatus(t, environ, p, Failed, &Run{Result: shell.Result{Status: "agent_failed"}})
		}, "failed"},
		{"gone", func(t *testing.T, environ []string, p Task) {
			dir, _ := store.Dir(environ, "task")
			if err := os.Remove(path(dir, p.ID)); err != nil {
				t.Fatal(err)
			}
		}, "does not exist"},
		{"done without a run", func(t *testing.T, environ []string, p Task) {
			setStatus(t, environ, p, Done, nil)
		}, "has no session"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			environ := env(t)
			p := create(t, environ, "claude")
			f, err := follow(environ, p.ID, Spec{})
			if err != nil {
				t.Fatal(err)
			}
			g, err := follow(environ, f.ID, Spec{})
			if err != nil {
				t.Fatal(err)
			}
			// g reads as older than f: the chain must not rely on created_at.
			g.CreatedAt = p.CreatedAt.Add(-time.Hour)
			setStatus(t, environ, g, Queued, nil)
			tt.parent(t, environ, p)
			tasks, _, err := List(environ)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{f.ID: p.ID + " " + tt.why, g.ID: f.ID + " failed"}
			for _, tk := range tasks {
				w, ok := want[tk.ID]
				if !ok {
					continue
				}
				if tk.Status != Failed || tk.Run == nil || tk.Run.Status != NotStarted || tk.Run.Message != "its parent task "+w {
					t.Errorf("%s = %+v %+v; want failed, its parent task %s", tk.ID, tk, tk.Run, w)
				}
				delete(want, tk.ID)
			}
			if len(want) > 0 {
				t.Errorf("not listed: %v", want)
			}
		})
	}
}

// A follow-up whose parent cannot be read waits; nothing is failed (P10).
func TestReclaimSparesFollowUpOfUnreadableParent(t *testing.T) {
	environ := env(t)
	p := create(t, environ, "claude")
	f, err := follow(environ, p.ID, Spec{})
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := store.Dir(environ, "task")
	if err := store.WriteFile(path(dir, p.ID), []byte("not json")); err != nil {
		t.Fatal(err)
	}
	tasks, notes, err := List(environ)
	if err != nil || len(tasks) != 1 || tasks[0].ID != f.ID || tasks[0].Status != Queued || !slices.Equal(notes.Skipped, []string{p.ID}) {
		t.Errorf("List = %+v %+v, %v; want %s queued and %s skipped", tasks, notes, err, f.ID, p.ID)
	}
}
