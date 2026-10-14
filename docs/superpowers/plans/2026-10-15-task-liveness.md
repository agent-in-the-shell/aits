# `aits task` worker liveness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Decide whether a `running` task's worker is alive by a per-task `flock` the worker holds, instead of a pid check after a wall-clock lease expiry.

**Architecture:**
- `internal/store` gains `TryLock`, a non-blocking `flock`.
- `internal/task`:
  - claim takes the task's lock before writing `running`;
  - the worker holds it until its run is recorded;
  - reclaim try-locks every running task's existing lock file, and fails the task (`worker_died`) only when it gets the lock.
- `internal/cli` prints a stderr line for running tasks whose lock file is missing.
- Help, `docs/cli.md`, P10 and the task-queue spec follow.

**Tech Stack:** Go 1.26, stdlib only (`syscall.Flock`); tests with the built binary and the fake vendor (`internal/shell/testdata/fakevendor`).

**Spec:** `docs/superpowers/specs/2026-10-15-task-liveness-design.md` (approved; revises `docs/superpowers/specs/2026-10-14-task-queue-design.md`)

## Global Constraints

- Stdlib only; no new dependency (P3).
- Linux and macOS behave the same; CI runs both (P13).
- Files 0600, created only by `internal/store` (P7; `internal/archtest/storage_test.go` forbids `os.WriteFile`, `os.MkdirAll` and `os.Create` outside it in non-test code).
- Release binary within `.github/binary-size-budget` (3,145,728 bytes); report the size change in the PR (P14).
- Lock files are never removed.
- A task lock is only ever taken with `LOCK_NB`.
- Reclaim never creates a lock file.
- Run status `worker_died`; message `the worker exited before it recorded a result`.
- Worker error: `task ID was reclaimed before its run was recorded`.
- Unknown line: `aits: cannot tell whether the worker of task ID is alive: its lock file is missing`, with the id escaped by `printable`.
- Lease JSON is exactly `{"token","pgid"}`.
- Commits: English, conventional, body `Refs #37`, attribution trailers.

## Review Focus

1. **A queued task whose lock someone else holds**, e.g. a task re-queued by hand while its old worker runs. `work` must fail (`internal`, exit 1), write nothing, and leave the task `queued`. Pinned in Task 2 (`TestClaimLockHeld`).
2. **A worker killed between claim and agent start (pgid 0).** It must be reclaimed as `worker_died` without signalling anything. Pinned in Task 2 (`TestReclaimDeadWorker` uses pgid 0).
3. **A running record written by an earlier aits**, with `worker_pid` and `expires_at` and no lock file. It must read, stay `running`, and be reported as unknown, never failed. Pinned in Task 2 (`TestReclaimMissingLockFile` writes the old lease shape).
4. **Many workers and lists at once.** Each task must be run exactly once and no live task failed. Pinned by the existing `TestTaskConcurrentWorkers`, which must stay green in Task 2.
5. **The agent outliving its killed worker.** The lock must be free at once, so one `list` fails the task and stops the agent. Pinned in Task 2 (`TestTaskWorkerKilled`).

---

### Task 1: `store.TryLock`

**Files:**
- Modify: `internal/store/store.go` (add `ErrLocked`, `TryLock` after `Lock`)
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `var ErrLocked = errors.New("locked by another process")`;
  - `func TryLock(path string, create bool) (func(), error)`, which returns the unlock function.
  - Error cases:
    - `ErrLocked` when another open file description holds the lock;
    - an `*os.PathError` wrapping `os.ErrNotExist` when `create` is false and `path` is missing; nothing is created;
    - otherwise the error.

- [ ] **Step 1: Write the failing test** — append to `internal/store/store_test.go` and add `"errors"` to its imports:

```go
func TestTryLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.lock")
	if _, err := TryLock(path, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("TryLock(missing, create=false) = %v, want ErrNotExist", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("TryLock(create=false) created the file")
	}
	unlock, err := TryLock(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if m := mode(t, path); m != 0o600 {
		t.Errorf("mode %v, want 0600", m)
	}
	for _, create := range []bool{true, false} {
		if _, err := TryLock(path, create); !errors.Is(err, ErrLocked) {
			t.Errorf("second TryLock(create=%v) = %v, want ErrLocked", create, err)
		}
	}
	unlock()
	again, err := TryLock(path, false)
	if err != nil {
		t.Fatalf("TryLock after unlock = %v", err)
	}
	again()
	loose := filepath.Join(dir, "b.lock")
	if err := os.WriteFile(loose, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := TryLock(loose, true)
	if err != nil {
		t.Fatal(err)
	}
	u()
	if m := mode(t, loose); m != 0o600 {
		t.Errorf("existing file mode %v, want 0600 (tightened)", m)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -count=1 -run TestTryLock ./internal/store`
Expected: FAIL to compile with `undefined: TryLock` and `undefined: ErrLocked`.

- [ ] **Step 3: Implement** — in `internal/store/store.go`, after `Lock`:

```go
// ErrLocked means another holder has the lock TryLock asked for.
var ErrLocked = errors.New("locked by another process")

// TryLock takes an exclusive lock on path without waiting and returns the
// function that releases it. With create it creates path, or tightens an
// existing file, to 0600; without it a missing path is an os.ErrNotExist
// error and nothing is created. Another holder gives ErrLocked.
func TryLock(path string, create bool) (func(), error) {
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	if create {
		if err := f.Chmod(0o600); err != nil {
			f.Close()
			return nil, err
		}
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return func() { f.Close() }, nil // closing the file releases the lock
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test -count=1 ./internal/store ./internal/archtest`
Expected: `ok` for both (the P7 guard still passes).

- [ ] **Step 5: Commit**

```bash
REF='#37' bash commit.sh "feat(store): TryLock, a lock that never waits" internal/store/store.go internal/store/store_test.go
```

---

### Task 2: Liveness by the task lock

**Files:**
- Modify: `internal/task/task.go`:
  - `NotStarted`/`LeaseExpired` consts become `NotStarted`/`WorkerDied`;
  - remove `leaseSlack`;
  - `Lease` struct;
  - `errReclaimed`;
  - `List`, `Work`, `claim`, `reclaim`, `snapshot`;
  - remove `alive`;
  - add `Notes` and `lockPath`;
  - drop the `syscall` import.
- Modify: `internal/cli/task.go` (list and work runners; add `reportUnknown`)
- Test:
  - `internal/task/task_test.go`:
    - replace `TestReclaim` and `TestReclaimSparesLiveWorker`;
    - add `TestReclaimMissingLockFile`, `TestReclaimLockError`, `TestClaimLockHeld` and the `running` helper;
    - remove `deadPID`.
  - `cmd/aits/task_test.go`:
    - `taskRecord.Lease`;
    - rewrite `TestTaskReclaim`;
    - extend `TestTaskWorkStopped`;
    - add `TestTaskWorkerKilled`, `TestTaskLockFileRemoved`, `TestTaskLockError` and the helpers `writeLockFile` and `waitLease`.

**Interfaces:**
- Consumes: `store.TryLock(path string, create bool) (func(), error)` and `store.ErrLocked` (Task 1).
- Produces:
  - `type Notes struct{ Skipped, Unknown []string }`;
  - `func List(environ []string) ([]Task, Notes, error)`;
  - `func Work(ctx context.Context, environ []string, agent string) (*Task, Notes, error)`;
  - `const WorkerDied shell.Status = "worker_died"`;
  - `type Lease struct{ Token string; PGID int }` with JSON `token`, `pgid`;
  - in `internal/cli`, `func reportUnknown(env *Env, ids []string)`.

- [ ] **Step 1: Write the failing unit tests.**
  - In `internal/task/task_test.go`, delete `TestReclaim`, `TestReclaimSparesLiveWorker` and `deadPID`.
  - Add `"fmt"` and `"slices"` to the imports, and remove `"os/exec"` if nothing else uses it.
  - Add:

```go
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
	unlock, err := store.TryLock(filepath.Join(dir, tk.ID+".lock"), true) // as a claim leaves it
	if err != nil {
		t.Fatal(err)
	}
	unlock()
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
	unlock, err := store.TryLock(filepath.Join(dir, tk.ID+".lock"), true)
	if err != nil {
		t.Fatal(err)
	}
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
	if _, err := os.Stat(filepath.Join(dir, tk.ID+".lock")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("reclaim made a lock file: %v", err)
	}
}

// A lock that cannot be tried fails list and changes no task (P6).
func TestReclaimLockError(t *testing.T) {
	environ := env(t)
	tk := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	running(t, dir, tk, `{"token":"w","pgid":0}`)
	if err := os.Mkdir(filepath.Join(dir, tk.ID+".lock"), 0o700); err != nil {
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
	unlock, err := store.TryLock(filepath.Join(dir, tk.ID+".lock"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if got, _, err := Work(context.Background(), environ, ""); err == nil {
		t.Fatalf("Work = %+v, want an error", got)
	}
	if got, _ := Show(environ, tk.ID); got.Status != Queued || got.Lease != nil {
		t.Errorf("task changed: %+v", got)
	}
}
```

Add `"bytes"` and `"encoding/json"` to the test imports if they are not already there.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -run 'TestReclaim|TestClaimLockHeld' ./internal/task`
Expected:
- FAIL to compile on `notes.Unknown`: `List` still returns `[]string`.
- To see the behavioural RED, temporarily comment out `TestReclaimMissingLockFile` and re-run. The remaining four fail:
  - `TestReclaimDeadWorker`: the live pid keeps the task running;
  - `TestReclaimSparesLiveWorker`: pid 0 and a zero `expires_at` reclaim it at once;
  - `TestReclaimLockError`: no error;
  - `TestClaimLockHeld`: `Work` claims.
- Restore the commented test.

- [ ] **Step 3: Write the failing functional tests** in `cmd/aits/task_test.go`.

1. Set `taskRecord.Lease` to:

```go
	Lease     *struct {
		Token string `json:"token"`
		PGID  int    `json:"pgid"`
	} `json:"lease"`
```

2. Add next to `writeTaskFile`:

```go
// writeLockFile leaves the lock file of id as a claim does, unlocked.
func (q queue) writeLockFile(id string) {
	q.t.Helper()
	if err := os.WriteFile(filepath.Join(q.home, "task", id+".lock"), nil, 0o600); err != nil {
		q.t.Fatal(err)
	}
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
```

3. Replace `TestTaskReclaim`:

```go
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
	file := filepath.Join(q.home, "task", id+".json")
	var rec map[string]any
	b, _ := os.ReadFile(file)
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	rec["status"] = "running"
	rec["lease"] = map[string]any{"token": "dead", "worker_pid": os.Getpid(), "pgid": sleeper.Process.Pid,
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	b, _ = json.Marshal(rec)
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
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
```

4. In `TestTaskWorkStopped`, after the existing lease check and before the SIGTERM, add:

```go
	var raw struct {
		Lease map[string]any `json:"lease"`
	}
	if err := json.Unmarshal([]byte(q.run("show", id, "--json").stdout), &raw); err != nil || len(raw.Lease) != 2 ||
		raw.Lease["token"] == nil || raw.Lease["pgid"] == nil {
		t.Errorf("lease = %v, want exactly token and pgid", raw.Lease)
	}
	if out := q.run("list", "--json"); out.code != 0 || decodeOne[taskList](t, out.stdout).Tasks[0].Status != "running" {
		t.Errorf("list while the worker runs: code=%d stdout=%q", out.code, out.stdout)
	}
```

5. Add:

```go
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
	if r := decodeOne[taskRecord](t, q.run("show", id, "--json").stdout); r.Status != "failed" || r.Run == nil || r.Run.Status != "worker_died" {
		t.Errorf("after the worker was killed: %+v", r)
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
	if err := os.Remove(filepath.Join(q.home, "task", id+".lock")); err != nil {
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
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	out := s.waitOrKill(t, 10*time.Second)
	if w := decodeOne[taskRecord](t, out.stdout); out.code != 1 || w.Status != "failed" || w.Run == nil || w.Run.Status != "stopped" {
		t.Errorf("work after SIGTERM: code=%d %+v", out.code, w)
	}
}

// A lock that cannot be tried fails list and work with internal and changes
// no task (P6).
func TestTaskLockError(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		q := newQueue(t, "ok")
		id := q.create("hi")
		file := filepath.Join(q.home, "task", id+".json")
		b, _ := os.ReadFile(file)
		b = []byte(strings.Replace(strings.Replace(string(b), `"status":"queued"`, `"status":"running"`, 1),
			`"lease":null`, `"lease":{"token":"w","pgid":0}`, 1))
		if err := os.WriteFile(file, b, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(q.home, "task", id+".lock"), 0o700); err != nil {
			t.Fatal(err)
		}
		out := q.run("list", "--json")
		if e := decode(t, out.stdout).Error; out.code != 1 || e == nil || e.Code != "internal" {
			t.Errorf("list: code=%d stdout=%q", out.code, out.stdout)
		}
		if after, _ := os.ReadFile(file); string(after) != string(b) {
			t.Errorf("record changed: %s", after)
		}
	})
	t.Run("queued", func(t *testing.T) {
		q := newQueue(t, "ok")
		id := q.create("hi")
		if err := os.Mkdir(filepath.Join(q.home, "task", id+".lock"), 0o700); err != nil {
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
```

- [ ] **Step 4: Run them to verify they fail**

Run: `go test -count=1 -run 'TestTaskReclaim|TestTaskWorkStopped|TestTaskWorkerKilled|TestTaskLockFileRemoved|TestTaskLockError' ./cmd/aits`
Expected: all FAIL against the current binary:
- `TestTaskReclaim`: the lease has not expired and the pid is alive;
- `TestTaskWorkStopped`: the lease has 4 keys, and strict decoding rejects `worker_pid`;
- `TestTaskWorkerKilled`: the task stays running;
- `TestTaskLockFileRemoved`: no stderr line;
- `TestTaskLockError`: no error.

- [ ] **Step 5: Implement** in `internal/task/task.go`.

1. Replace the run-status block and remove `leaseSlack`:

```go
// Run statuses besides shell's: the agent was never started after the claim,
// or the worker exited before it recorded a result.
const (
	NotStarted shell.Status = "not_started"
	WorkerDied shell.Status = "worker_died"
)
```

2. `Lease`:

```go
// Lease is a worker's claim on a running task. Whether the worker is alive is
// not recorded: it holds the task's lock file while it runs (lockPath).
type Lease struct {
	Token string `json:"token"`
	PGID  int    `json:"pgid"` // the agent's process group; 0 until it starts
}
```

3. `errReclaimed`:

```go
// errReclaimed means another caller failed the task, its worker being gone,
// before this worker recorded its run.
var errReclaimed = errors.New("reclaimed before its run was recorded")
```

4. Add after `errReclaimed`:

```go
// Notes are what list and work report besides tasks, as sorted ids: task
// files that could not be read, and running tasks whose lock file is missing,
// so that whether their worker is alive cannot be told.
type Notes struct{ Skipped, Unknown []string }

// lockPath is the lock file a task's worker holds while it runs. It is made
// by the claim and never removed: removing a held lock file would let a new
// one at the same path be locked by someone else.
func lockPath(dir, id string) string { return filepath.Join(dir, id+".lock") }
```

5. `List`: change the signature to `func List(environ []string) ([]Task, Notes, error)`. Change the comment's last clause to "…oldest first, and its notes." Make the early returns `return nil, Notes{}, err`, and the end `return s.tasks, s.Notes, err`.

6. `Work`:
   - Change the signature to `func Work(ctx context.Context, environ []string, agent string) (*Task, Notes, error)`.
   - Change the doc sentence "It also returns the ids of the task files it skipped as unreadable." to "It also returns the notes of the reclaim (Notes)."
   - Make the early returns `return nil, Notes{}, err`.
   - Replace the claim call and its check with:

```go
	t, release, notes, err := claim(ctx, dir, agent)
	if t == nil || err != nil {
		return nil, notes, err
	}
	defer release() // the worker holds the task's lock until its run is recorded
```

   - Change the two later returns to `return nil, notes, err` and `return &done, notes, nil`.

7. `claim`:

```go
// claim, under the lock, reclaims tasks of dead workers and marks the oldest
// queued task of agent running with a fresh lease. It locks the task's lock
// file first, so that a running task's lock is never free while its worker
// lives, and returns the function that releases it, with the notes of the
// reclaim. A signal that arrived while it waited for the lock claims nothing.
func claim(ctx context.Context, dir, agent string) (*Task, func(), Notes, error) {
	unlock, err := store.Lock(dir)
	if err != nil {
		return nil, nil, Notes{}, err
	}
	if ctx.Err() != nil {
		unlock()
		return nil, nil, Notes{}, shell.ErrStopped
	}
	s, groups, err := reclaim(dir)
	var claimed *Task
	var release func()
	if err == nil {
		for i := range s.tasks {
			t := &s.tasks[i]
			if t.Status != Queued || (agent != "" && t.Agent != agent) {
				continue
			}
			if release, err = store.TryLock(lockPath(dir, t.ID), true); err != nil {
				err = fmt.Errorf("task %s: %w", t.ID, err)
				break
			}
			t.Status = Running
			t.Lease = &Lease{Token: fmt.Sprintf("%016x", rand.Uint64())}
			if err = write(dir, *t); err != nil {
				release()
				release = nil
				break
			}
			claimed = t
			break
		}
	}
	unlock()
	shell.Stop(groups...)
	return claimed, release, s.Notes, err
}
```

8. `reclaim`:

```go
// reclaim, under the lock, loads every task and fails each running one whose
// lock file it can lock: no worker holds it, so the worker is gone. A held
// lock is a live worker, whatever its age (P10); a missing lock file cannot be
// judged and is noted (P6); any other error stops it. It returns what it
// loaded and the agent process groups to stop once the lock is free.
func reclaim(dir string) (snapshot, []int, error) {
	s, err := load(dir)
	if err != nil {
		return s, nil, err
	}
	var groups []int
	for i := range s.tasks {
		t := &s.tasks[i]
		if t.Status != Running {
			continue
		}
		release, err := store.TryLock(lockPath(dir, t.ID), false)
		switch {
		case errors.Is(err, store.ErrLocked):
			continue
		case errors.Is(err, os.ErrNotExist):
			s.Unknown = append(s.Unknown, t.ID)
			continue
		case err != nil:
			return s, groups, fmt.Errorf("task %s: %w", t.ID, err)
		}
		groups = append(groups, t.Lease.PGID)
		t.Status, t.Lease = Failed, nil
		t.Run = &Run{Result: shell.Result{
			Status: WorkerDied, Agent: t.Agent,
			Message: "the worker exited before it recorded a result",
		}, FinishedAt: time.Now().UTC()}
		err = write(dir, *t)
		release()
		if err != nil {
			return s, groups, err
		}
	}
	slices.Sort(s.Unknown)
	return s, groups, nil
}
```

9. Delete `alive` and remove `"syscall"` from the imports.

10. `snapshot` and `load`: the struct becomes

```go
type snapshot struct {
	tasks []Task // every readable task, oldest first by created_at, then by id
	Notes
}
```

    In `load`, replace `s.skipped` with `s.Skipped` (both the append and `slices.Sort(s.Skipped)`).

- [ ] **Step 6: Implement** in `internal/cli/task.go`.

1. In the list runner, replace `tasks, skipped, err := task.List(env.Environ)` with `tasks, notes, err := task.List(env.Environ)`. Inside the JSON branch, put `reportUnknown(env, notes.Unknown)` before the JSON is written and use `notes.Skipped` in the struct literal. In text mode, call `reportSkipped(env, notes.Skipped)` then `reportUnknown(env, notes.Unknown)`.

2. In the work runner:

```go
				t, notes, err := task.Work(ctx, env.Environ, *agent)
				reportSkipped(env, notes.Skipped)
				reportUnknown(env, notes.Unknown)
```

3. After `reportSkipped`:

```go
// reportUnknown names, on stderr, each running task whose lock file is
// missing, so that whether its worker is alive cannot be told.
func reportUnknown(env *Env, ids []string) {
	for _, id := range ids {
		fmt.Fprintf(env.Stderr, "aits: cannot tell whether the worker of task %s is alive: its lock file is missing\n", printable(id))
	}
}
```

- [ ] **Step 7: Run everything**

Run: `gofmt -l . ; go vet ./... && staticcheck -tags live ./... && go test -count=1 ./...`
Expected:
- `gofmt` prints nothing;
- every package is `ok`, including the five functional tests from Step 3, the five unit tests from Step 1, `TestTaskConcurrentWorkers` and `TestOutputChannels`.

`TestCLIDocs` still passes: help text is unchanged until Task 3.

- [ ] **Step 8: Commit**

```bash
REF='#37' bash commit.sh "feat(task): a worker's liveness is the lock it holds, not its pid or lease" internal/task/task.go internal/task/task_test.go internal/cli/task.go cmd/aits/task_test.go
```

---

### Task 3: Help, docs, P10 and the task-queue spec

**Files:**
- Modify:
  - `internal/cli/task.go`: the `taskFields` lease and `run.status` lines, the list `Details` sentence about reclaim, and the work `Details` sentence about the reclaimed worker;
  - `docs/cli.md` (regenerated);
  - `docs/principles.md` (P10);
  - `docs/superpowers/specs/2026-10-14-task-queue-design.md`.
- Test: `cmd/aits/docs_test.go` (`TestCLIDocs`, existing)

**Interfaces:**
- Consumes: the behaviour of Task 2.
- Produces: documentation only.

- [ ] **Step 1: Update the help** in `internal/cli/task.go`. In `taskFields`, replace

```
  lease        While running: token, worker_pid, pgid, expires_at.
```
with
```
  lease        While running: token, pgid.
```
and
```
               not_started (the agent was never started; no started_at), or
               lease_expired (the worker died; set by the next work or list).
```
with
```
               not_started (the agent was never started; no started_at), or
               worker_died (its worker exited first; set by the next work or
               list).
```

In list `Details`, replace `Expired leases of dead workers are reclaimed first.` with

```
Tasks of dead workers are reclaimed first. A running task whose lock file is
missing cannot be judged: it stays running, with "aits: cannot tell whether
the worker of task ID is alive: its lock file is missing" on stderr.
```

In work `Details`, replace `A worker whose lease was reclaimed records nothing
and fails with "task ID was reclaimed after its lease expired".` with
`A worker whose task was reclaimed records nothing and fails with "task ID
was reclaimed before its run was recorded".`, re-wrapped to the file's
width. After the skip sentence, add `Running tasks whose lock file is
missing are reported as list does.`

- [ ] **Step 2: Run the docs test to see it fail**

Run: `go test -count=1 -run TestCLIDocs ./cmd/aits`
Expected: FAIL, because `docs/cli.md` differs from help.

- [ ] **Step 3: Regenerate and re-run**

Run: `go test -count=1 ./cmd/aits -run TestCLIDocs -update && go test -count=1 -run TestCLIDocs ./cmd/aits`
Expected: `ok`. `git diff docs/cli.md` shows only the lines changed in Step 1.

- [ ] **Step 4: P10** in `docs/principles.md`:
  - replace `(for example, an expiring lease)` with `(for example, a lock the kernel releases when its holder exits)`;
  - replace `a test asserts an expired lease can be
reclaimed.` with `a test asserts the task of a dead worker is
reclaimed, even when its pid now belongs to a live process.`, re-wrapped.

- [ ] **Step 5: Revise the task-queue spec in place**, `docs/superpowers/specs/2026-10-14-task-queue-design.md`:
  - under the header, add `- Revised: worker liveness by #37, see 2026-10-15-task-liveness-design.md`;
  - §2 rows "Lease" and "Expired lease" become:
    - `Lease | Written at claim: random token and the agent's pgid; the worker holds the task's lock file <id>.lock until its run is recorded | P10: the kernel releases the lock however the worker exits`;
    - `Dead worker | Task set to failed (worker_died), its leftover agent process group stopped; never re-queued | Re-running a task with side effects twice is worse than asking the dispatcher to resubmit`;
  - §3: in the `internal/store` list, "three functions" becomes "four functions", with a `TryLock(path, create)` bullet after `Lock`;
  - §4: the `lease` bullet becomes `{"token","pgid"}` while `running`; in the `run.status` bullet, `lease_expired when the worker died` becomes `worker_died when the worker died`;
  - §6:
    - Claim: lock `<id>.lock` (create) before writing `running`; the lease has no expiry;
    - Record: the reclaimed worker's message is `aits: task ID was reclaimed before its run was recorded`;
    - Reclaim: replace the expiry/alive sentence with try-locking the existing lock file (obtained → `worker_died`; held → left; missing → reported; other error → fail);
    - Known limits: add the four limits from the liveness spec §5;
  - §8: test 6 becomes "reclaim: a hand-written `running` record whose lock file nobody holds, with an alive `worker_pid` and the pgid of a sleeping process; `list` makes it `failed` (`worker_died`) and the process is gone", and add tests from the liveness spec §6;
  - §7 P10 guard bullet, if present: same wording as the new P10 guard.

- [ ] **Step 6: Verify and commit**

Run: `go test -count=1 ./... && grep -rn "lease_expired\|expires_at\|worker_pid\|after its lease expired" internal cmd docs/cli.md`
Expected: tests `ok`; grep prints only test lines that write the old lease shape on purpose (`internal/task/task_test.go`, `cmd/aits/task_test.go`).

```bash
REF='#37' bash commit.sh "docs(task): liveness by lock in help, P10 and the task-queue spec" internal/cli/task.go docs/cli.md docs/principles.md docs/superpowers/specs/2026-10-14-task-queue-design.md
```

---

## Final verification (after Task 3)

- **Mutation checks**: one worktree per mutation; each must make at least one test fail:
  - reclaim calls `TryLock(…, true)`;
  - claim writes `running` before `TryLock`;
  - reclaim treats a missing lock file as dead;
  - reclaim ignores a lock error;
  - `Work` releases the lock before the record step;
  - the agent is started with the lock fd in `ExtraFiles`.
- **Adversarial pass** on kill timing and concurrency against the built binary:
  - SIGKILL at each step of claim, run and record;
  - many workers with `list` loops;
  - a lock file replaced by a directory, a symlink or a FIFO.
- **Size**: the release binary size in CI against main, reported in the PR.
