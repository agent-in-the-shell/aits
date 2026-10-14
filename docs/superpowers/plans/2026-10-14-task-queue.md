# `aits task` Implementation Plan

> Executed. Since then, `create` validates through `shell.Request.Validate`
> and reclaim stops groups through `shell.Stop`; the code is the reference.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `aits task create|list|show|work`: a local queue of agent runs, stored as one JSON file per task, claimed under a `flock`, run through `shell`, with leases that the next caller reclaims.

**Architecture:** `internal/store` owns every write (P7). `internal/task` holds the queue: records, claim, reclaim, run. `internal/shell` gains a `Started` callback so a worker can record the agent's process group. `internal/cli/task.go` is the command group. Two guards land with it: the P1 import graph and the P7 write check.

**Tech Stack:** Go 1.26 standard library (`encoding/json`, `os`, `syscall.Flock`, `math/rand/v2`).

**Spec:** `docs/superpowers/specs/2026-10-14-task-queue-design.md`. Issue: #32.

## Global Constraints

- Standard library only; no cgo; the release binary stays within `.github/binary-size-budget` (P3, P14). Expected growth about 40 KB.
- No new error codes: `invalid_input`, `unknown_flag`, `stdin_failed`, `stopped`, `internal` only.
- Files under `$AITS_HOME/task/`, directories 0700, files 0600, writes through temp file plus rename; only `internal/store` calls `os.WriteFile`, `os.MkdirAll`, `os.Create` (P7).
- `--env` values are never stored, only names (P8). No permission flags (P9).
- Tests run the built binary with a temporary `HOME` and `AITS_HOME` (P12); tests stay proportionate (P14).
- Commits leak-checked, body `Refs #32`.

## Review Focus

1. Two workers racing for the last queued task: exactly one runs it, the other prints `{"status":"empty"}` — pinned by the concurrent functional test (Task 4).
2. A worker that finishes after its lease was reclaimed must not overwrite the `failed` record — pinned by `TestUpdateForeignToken` (Task 3).
3. A corrupt or half-written task file must not be produced by a crash mid-write — pinned by `TestWriteFileLeavesNoTemp` and the rename in `store.WriteFile` (Task 1).
4. Reclaim must stop a still-running leftover agent group — pinned by the reclaim functional test with a live process group (Task 4).
5. `--env NAME` unset in the worker's environment must fail the task as `not_started`, not crash or run without it — pinned by `TestWorkNotStarted` (Task 3).

---

### Task 1: `internal/store` and the P7 write guard

**Files:** Create `internal/store/store.go`, `internal/store/store_test.go`, `internal/archtest/storage_test.go`.

**Interfaces — Produces:** `store.Dir(environ []string, group string) (string, error)`, `store.WriteFile(path string, data []byte) error`, `store.Lock(dir string) (unlock func(), err error)`.

- [ ] **Step 1: tests**

`internal/store/store_test.go`:

```go
package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "aits")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := Dir([]string{"HOME=/nowhere", "AITS_HOME=" + root}, "task")
	if err != nil || dir != filepath.Join(root, "task") {
		t.Fatalf("Dir = %q, %v", dir, err)
	}
	if m := mode(t, root); m != 0o700 {
		t.Errorf("root mode %v, want 0700 (tightened)", m)
	}
	if m := mode(t, dir); m != 0o700 {
		t.Errorf("dir mode %v, want 0700", m)
	}
	home := t.TempDir()
	if dir, err := Dir([]string{"HOME=" + home}, "task"); err != nil || dir != filepath.Join(home, ".local/share/aits/task") {
		t.Errorf("default Dir = %q, %v", dir, err)
	}
	if _, err := Dir(nil, "task"); err == nil {
		t.Error("Dir without HOME or AITS_HOME succeeded")
	}
}

func TestWriteFileLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.json")
	for _, data := range []string{"one", "two"} {
		if err := WriteFile(path, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(path); string(b) != "two" {
		t.Errorf("content %q", b)
	}
	if m := mode(t, path); m != 0o600 {
		t.Errorf("mode %v, want 0600", m)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only a.json", len(entries))
	}
}

func TestLock(t *testing.T) {
	dir := t.TempDir()
	unlock, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		u, err := Lock(dir)
		if err == nil {
			u()
		}
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("a second Lock did not wait for the first")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the second Lock did not get the lock after unlock")
	}
}
```

`internal/archtest/storage_test.go`:

```go
package archtest

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestOnlyStoreWrites enforces P7: only internal/store creates directories or
// writes files, so the root, the modes and the temp-file-plus-rename rule
// live in one place.
func TestOnlyStoreWrites(t *testing.T) {
	write := regexp.MustCompile(`\bos\.(WriteFile|MkdirAll|Create)\(`)
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "testdata" || d.Name() == ".git" || path == filepath.Join("..", "..", "internal", "store")) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if loc := write.FindIndex(src); loc != nil {
			t.Errorf("%s writes files outside internal/store: %s", path, src[loc[0]:loc[1]])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: RED** — `go test ./internal/store` fails to build (`undefined: Dir`); `go test ./internal/archtest -run TestOnlyStoreWrites` passes today (no non-test writer exists), which is expected: it guards future code.

- [ ] **Step 3: `internal/store/store.go`**

```go
// Package store owns every file aits writes (P7): one root, $AITS_HOME
// (default ~/.local/share/aits), with one directory per command group;
// directories 0700, files 0600; writes go through a temporary file and a
// rename, so a reader sees the old file or the new one, never half of one.
package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Dir returns the directory of group under the root, creating it 0700 and
// tightening the root and the directory to 0700 if they exist.
func Dir(environ []string, group string) (string, error) {
	root := lookup(environ, "AITS_HOME")
	if root == "" {
		home := lookup(environ, "HOME")
		if home == "" {
			return "", errors.New("neither AITS_HOME nor HOME is set")
		}
		root = filepath.Join(home, ".local", "share", "aits")
	}
	dir := filepath.Join(root, group)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, d := range []string{root, dir} {
		if err := os.Chmod(d, 0o700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// WriteFile replaces path with data through a temporary file in the same
// directory and a rename. The file is 0600.
func WriteFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}

// Lock takes an exclusive lock on dir/.lock, waiting for it if another
// process holds it, and returns the function that releases it.
func Lock(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil // closing the file releases the lock
}

// lookup returns the last value of name in environ, or "".
func lookup(environ []string, name string) string {
	val := ""
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			val = v
		}
	}
	return val
}
```

- [ ] **Step 4: GREEN** — `gofmt -l .; go vet ./... && go test -count=1 ./internal/store ./internal/archtest` → ok.

- [ ] **Step 5: commit** `feat(store): one storage package for aits files (P7)` — `internal/store internal/archtest/storage_test.go`.

---

### Task 2: `shell.Request.Started`

**Files:** Modify `internal/shell/shell.go` (Request), `internal/shell/process.go` (run). Test lands in Task 4 (the functional SIGTERM test reads the recorded pgid).

**Interfaces — Produces:** `shell.Request.Started func(pgid int)`.

- [ ] **Step 1: add the field** after `Session` in `Request`:

```go
	Started func(pgid int) // if set, called once with the agent's process group right after it starts
```

- [ ] **Step 2: call it** in `run`, right after the successful `cmd.Start()` (after the pipes are closed, before `supervise`):

```go
	if req.Started != nil {
		req.Started(cmd.Process.Pid) // the agent leads its own group (Setpgid)
	}
```

- [ ] **Step 3: GREEN** — `go vet ./... && go test -count=1 ./internal/shell ./cmd/aits` → ok (no behaviour change when nil).

- [ ] **Step 4: commit** `feat(shell): report the agent's process group to the caller once it starts` — `internal/shell`.

---

### Task 3: `internal/task`

**Files:** Create `internal/task/task.go`, `internal/task/task_test.go`.

**Interfaces — Consumes:** `store.*` (Task 1), `shell.Request.Started` (Task 2), `shell.Submit/Result/Status/Agents/ErrStopped/StatusOK`.
**Produces:** types `Task`, `Lease`, `Run`, `Spec`, `Status` (`Queued`, `Running`, `Done`, `Failed`), run statuses `NotStarted`, `LeaseExpired` (`shell.Status`), `*InvalidError{Reason}`, `ErrReclaimed`; functions `Create(environ, Spec) (Task, error)`, `List(environ) ([]Task, error)`, `Show(environ, id) (Task, error)`, `Work(ctx, environ, agent) (*Task, error)` (nil, nil = nothing queued).

- [ ] **Step 1: tests** — `internal/task/task_test.go`:

```go
package task

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-in-the-shell/aits/internal/store"
)

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
		"bad env name":  {Agent: "claude", Cwd: dir, Prompt: "hi", Timeout: time.Minute, Env: []string{"A=B"}},
	} {
		var invalid *InvalidError
		if _, err := Create(environ, s); !errors.As(err, &invalid) {
			t.Errorf("%s: err = %v, want *InvalidError", name, err)
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
	got, err := Work(context.Background(), environ, "codex")
	if err != nil || got == nil || got.ID != b.ID {
		t.Fatalf("Work(codex) = %+v, %v; want %s", got, err, b.ID)
	}
	if got, _ := Work(context.Background(), environ, ""); got == nil || got.ID != a.ID {
		t.Fatalf("Work() claimed %+v, want %s", got, a.ID)
	}
	if got, err := Work(context.Background(), environ, ""); got != nil || err != nil {
		t.Fatalf("Work on an empty queue = %+v, %v", got, err)
	}
}

func TestWorkNotStarted(t *testing.T) {
	environ := env(t)
	tk, err := Create(environ, Spec{Agent: "claude", Cwd: t.TempDir(), Prompt: "hi", Timeout: time.Minute, Env: []string{"AITS_TEST_UNSET"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Work(context.Background(), environ, "")
	if err != nil || got.ID != tk.ID || got.Status != Failed || got.Run == nil || got.Run.Status != NotStarted || got.Run.Message == "" || got.Lease != nil {
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
		x.tk.Status, x.tk.Lease = Running, &Lease{Token: "w", WorkerPID: 1, ExpiresAt: x.expires}
		if err := write(dir, x.tk); err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := List(environ)
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
	if _, err := update(dir, tk.ID, "mine", func(t *Task) { t.Status = Done }); !errors.Is(err, ErrReclaimed) {
		t.Fatalf("update = %v, want ErrReclaimed", err)
	}
	if got, _ := Show(environ, tk.ID); got.Status != Running {
		t.Errorf("record overwritten: %+v", got)
	}
}

func TestShowUnknown(t *testing.T) {
	var invalid *InvalidError
	for _, id := range []string{"nope", "../../etc/passwd"} {
		if _, err := Show(env(t), id); !errors.As(err, &invalid) {
			t.Errorf("Show(%q) err = %v", id, err)
		}
	}
}
```

- [ ] **Step 2: RED** — `go test ./internal/task` fails to build (`undefined: Create`).

- [ ] **Step 3: `internal/task/task.go`**

```go
// Package task is the "task" command group: a local queue of agent runs. A
// dispatcher creates tasks; workers claim and run one task per call through
// shell; results are read back by id (spec 2026-10-14-task-queue-design).
package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/agent-in-the-shell/aits/internal/shell"
	"github.com/agent-in-the-shell/aits/internal/store"
)

// Status is where a task is in its life.
type Status string

const (
	Queued  Status = "queued"
	Running Status = "running"
	Done    Status = "done"
	Failed  Status = "failed"
)

// Run statuses besides shell's: the agent was never started after the claim,
// or the worker's lease expired before it recorded a result.
const (
	NotStarted   shell.Status = "not_started"
	LeaseExpired shell.Status = "lease_expired"
)

// leaseSlack is how long past the task's own timeout a lease lasts: shell
// stops the agent at the timeout, so only a dead worker outlives its lease.
const leaseSlack = time.Minute

// stopGrace is how long a leftover agent group gets between TERM and KILL.
const stopGrace = 5 * time.Second

// Task is one queued agent run and, once worked, its result.
type Task struct {
	ID        string    `json:"id"`
	Status    Status    `json:"status"`
	Agent     string    `json:"agent"`
	Cwd       string    `json:"cwd"`
	Prompt    string    `json:"prompt"`
	TimeoutMS int64     `json:"timeout_ms"`
	Env       []string  `json:"env,omitempty"` // names only (P8)
	CreatedAt time.Time `json:"created_at"`
	Lease     *Lease    `json:"lease"`
	Run       *Run      `json:"run"`
}

// Lease is a worker's claim on a running task.
type Lease struct {
	Token     string    `json:"token"`
	WorkerPID int       `json:"worker_pid"`
	PGID      int       `json:"pgid"` // the agent's process group; 0 until it starts
	ExpiresAt time.Time `json:"expires_at"`
}

// Run is the outcome of working a task.
type Run struct {
	shell.Result
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// Spec is a task to create.
type Spec struct {
	Agent, Cwd, Prompt string
	Timeout            time.Duration
	Env                []string
}

// InvalidError means the input was rejected and nothing was written.
type InvalidError struct{ Reason string }

func (e *InvalidError) Error() string { return e.Reason }

func invalid(format string, a ...any) error { return &InvalidError{fmt.Sprintf(format, a...)} }

// ErrReclaimed means the worker's lease expired and another caller failed the
// task before the worker could record its run.
var ErrReclaimed = errors.New("the task was reclaimed after its lease expired")

// Create validates s and queues it.
func Create(environ []string, s Spec) (Task, error) {
	if err := s.validate(); err != nil {
		return Task{}, err
	}
	dir, err := store.Dir(environ, "task")
	if err != nil {
		return Task{}, err
	}
	unlock, err := store.Lock(dir)
	if err != nil {
		return Task{}, err
	}
	defer unlock()
	now := time.Now().UTC()
	t := Task{
		Status: Queued, Agent: s.Agent, Cwd: s.Cwd, Prompt: s.Prompt,
		TimeoutMS: s.Timeout.Milliseconds(), Env: s.Env, CreatedAt: now,
	}
	for {
		t.ID = fmt.Sprintf("%s-%06x", now.Format("20060102T150405"), rand.Uint32()&0xffffff)
		if _, err := os.Stat(path(dir, t.ID)); errors.Is(err, os.ErrNotExist) {
			break
		}
	}
	return t, write(dir, t)
}

func (s Spec) validate() error {
	switch {
	case s.Agent == "":
		return invalid("agent is required (one of: %s)", strings.Join(shell.Agents(), ", "))
	case !slices.Contains(shell.Agents(), s.Agent):
		return invalid("unknown agent %q (one of: %s)", s.Agent, strings.Join(shell.Agents(), ", "))
	case strings.TrimSpace(s.Prompt) == "":
		return invalid("prompt is empty")
	case !filepath.IsAbs(s.Cwd):
		return invalid("working directory %q is not absolute", s.Cwd)
	case s.Timeout <= 0:
		return invalid("timeout must be positive, got %s", s.Timeout)
	}
	if fi, err := os.Stat(s.Cwd); err != nil || !fi.IsDir() {
		return invalid("working directory %q is not a directory", s.Cwd)
	}
	for _, name := range s.Env {
		if name == "" || strings.Contains(name, "=") {
			return invalid("%q is not an environment variable name", name)
		}
	}
	return nil
}

// List reclaims expired leases and returns every task, oldest first.
func List(environ []string) ([]Task, error) {
	dir, err := store.Dir(environ, "task")
	if err != nil {
		return nil, err
	}
	unlock, err := store.Lock(dir)
	if err != nil {
		return nil, err
	}
	tasks, err := load(dir)
	var groups []int
	if err == nil {
		groups, err = reclaim(dir, tasks)
	}
	unlock()
	stop(groups)
	return tasks, err
}

// Show returns one task; an unknown id is an *InvalidError.
func Show(environ []string, id string) (Task, error) {
	dir, err := store.Dir(environ, "task")
	if err != nil {
		return Task{}, err
	}
	if strings.ContainsAny(id, `/\`) {
		return Task{}, invalid("no task %s", id)
	}
	t, err := read(path(dir, id))
	if errors.Is(err, os.ErrNotExist) {
		return Task{}, invalid("no task %s", id)
	}
	return t, err
}

// Work reclaims expired leases, claims the oldest queued task (of agent, if
// not empty), runs it through shell and records the run. It returns nil when
// nothing is queued, shell.ErrStopped when ctx is done before the claim, and
// ErrReclaimed when the lease was lost.
func Work(ctx context.Context, environ []string, agent string) (*Task, error) {
	if agent != "" && !slices.Contains(shell.Agents(), agent) {
		return nil, invalid("unknown agent %q (one of: %s)", agent, strings.Join(shell.Agents(), ", "))
	}
	dir, err := store.Dir(environ, "task")
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, shell.ErrStopped
	}
	t, err := claim(dir, agent)
	if t == nil || err != nil {
		return nil, err
	}
	token := t.Lease.Token
	started := time.Now().UTC()
	res, err := shell.Submit(ctx, shell.Request{
		Agent: t.Agent, Prompt: t.Prompt, Dir: t.Cwd, Timeout: time.Duration(t.TimeoutMS) * time.Millisecond,
		Environ: environ, Pass: t.Env,
		Started: func(pgid int) {
			_, _ = update(dir, t.ID, token, func(t *Task) { t.Lease.PGID = pgid })
		},
	})
	if err != nil {
		res = shell.Result{Status: NotStarted, Agent: t.Agent, Message: err.Error()}
	}
	status := Failed
	if res.Status == shell.StatusOK {
		status = Done
	}
	run := &Run{Result: res, StartedAt: started, FinishedAt: time.Now().UTC()}
	done, err := update(dir, t.ID, token, func(t *Task) { t.Status, t.Run, t.Lease = status, run, nil })
	if err != nil {
		return nil, err
	}
	return &done, nil
}

// claim, under the lock, reclaims expired leases and marks the oldest queued
// task of agent running with a fresh lease.
func claim(dir, agent string) (*Task, error) {
	unlock, err := store.Lock(dir)
	if err != nil {
		return nil, err
	}
	tasks, err := load(dir)
	var groups []int
	if err == nil {
		groups, err = reclaim(dir, tasks)
	}
	var claimed *Task
	if err == nil {
		for i := range tasks {
			if t := &tasks[i]; t.Status == Queued && (agent == "" || t.Agent == agent) {
				t.Status = Running
				t.Lease = &Lease{
					Token:     fmt.Sprintf("%016x", rand.Uint64()),
					WorkerPID: os.Getpid(),
					ExpiresAt: time.Now().UTC().Add(time.Duration(t.TimeoutMS)*time.Millisecond + leaseSlack),
				}
				if err = write(dir, *t); err == nil {
					claimed = t
				}
				break
			}
		}
	}
	unlock()
	stop(groups)
	return claimed, err
}

// reclaim fails every running task whose lease has expired, updating tasks in
// place, and returns the agent process groups to stop once the lock is free.
func reclaim(dir string, tasks []Task) ([]int, error) {
	var groups []int
	now := time.Now()
	for i := range tasks {
		t := &tasks[i]
		if t.Status != Running || t.Lease == nil || now.Before(t.Lease.ExpiresAt) {
			continue
		}
		if t.Lease.PGID > 0 {
			groups = append(groups, t.Lease.PGID)
		}
		t.Status, t.Lease = Failed, nil
		t.Run = &Run{Result: shell.Result{
			Status: LeaseExpired, Agent: t.Agent,
			Message: "the worker's lease expired before it recorded a result",
		}, FinishedAt: now.UTC()}
		if err := write(dir, *t); err != nil {
			return groups, err
		}
	}
	return groups, nil
}

// stop ends leftover agent process groups: TERM, up to stopGrace, KILL (P10).
func stop(groups []int) {
	for _, pgid := range groups {
		if syscall.Kill(-pgid, syscall.SIGTERM) != nil {
			continue // already gone
		}
		for deadline := time.Now().Add(stopGrace); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
				break
			}
		}
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
}

// update applies fn to task id under the lock if the caller still holds its
// lease (token), and writes it back.
func update(dir, id, token string, fn func(*Task)) (Task, error) {
	unlock, err := store.Lock(dir)
	if err != nil {
		return Task{}, err
	}
	defer unlock()
	t, err := read(path(dir, id))
	if err != nil {
		return Task{}, err
	}
	if t.Lease == nil || t.Lease.Token != token {
		return t, ErrReclaimed
	}
	fn(&t)
	return t, write(dir, t)
}

// load reads every task in dir, oldest first (ids sort by creation time).
func load(dir string) ([]Task, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var tasks []Task
	for _, e := range entries {
		if name := e.Name(); strings.HasSuffix(name, ".json") && !strings.HasPrefix(name, ".") {
			t, err := read(filepath.Join(dir, name))
			if err != nil {
				return nil, err
			}
			tasks = append(tasks, t)
		}
	}
	return tasks, nil
}

func path(dir, id string) string { return filepath.Join(dir, id+".json") }

func read(file string) (Task, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return Task{}, err
	}
	var t Task
	if err := json.Unmarshal(b, &t); err != nil {
		return Task{}, fmt.Errorf("%s: %w", filepath.Base(file), err)
	}
	return t, nil
}

func write(dir string, t Task) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return store.WriteFile(path(dir, t.ID), append(b, '\n'))
}
```

- [ ] **Step 4: GREEN** — `gofmt -l .; go vet ./... && go test -count=1 -race ./internal/task` → ok.

- [ ] **Step 5: commit** `feat(task): queue, claim, lease and reclaim` — `internal/task`.

---

### Task 4: CLI, functional tests, P1 guard, docs

**Files:** Create `internal/cli/task.go`, `cmd/aits/task_test.go`, `internal/archtest/graph_test.go`. Modify `internal/cli/command.go` (Root children), `README.md`, `docs/cli.md` (regenerated).

**Interfaces — Consumes:** `task.*` (Task 3); cli `nonEmpty`, `readPrompt`, `usageError`, `failure`, `writeJSON`, `exitOK`, `exitFailure`; test helpers `call`, `vendor`, `decodeOne`, `readRecord`, `waitFile`, `readPID`, `assertGone`, `finish`.

- [ ] **Step 1: functional tests** — `cmd/aits/task_test.go`:

```go
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

type taskRecord struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Agent  string `json:"agent"`
	Cwd    string `json:"cwd"`
	Lease  *struct {
		PGID int `json:"pgid"`
	} `json:"lease"`
	Run *struct {
		Status    string `json:"status"`
		SessionID string `json:"session_id"`
		Answer    string `json:"answer"`
		Message   string `json:"message"`
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

func (q queue) create(prompt string) string {
	q.t.Helper()
	out := q.run("create", "--agent", "claude", "--cwd", q.t.TempDir(), "--json", prompt)
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
		w.Run.SessionID != "sess-fake-1" || w.Lease != nil {
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
	rec["lease"] = map[string]any{"token": "dead", "worker_pid": 1, "pgid": sleeper.Process.Pid,
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
```

The test file writes a task record directly with `os.WriteFile`; `TestOnlyStoreWrites` skips `_test.go` files, so this is allowed.

`internal/archtest/graph_test.go`:

```go
package archtest

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/agent-in-the-shell/aits/internal/"

// allowed is the declared dependency graph between command groups (P1): the
// groups each group may import. Shared packages (cli, store, archtest) are
// not groups.
var allowed = map[string][]string{
	"shell":   nil,
	"session": nil,
	"task":    {"shell"},
}

// TestGroupDependencies enforces P1: every import between command groups is
// declared above, and the declared graph has no cycle.
func TestGroupDependencies(t *testing.T) {
	cmd := exec.Command("go", "list", "-f", `{{.ImportPath}} {{join .Imports " "}}`, "./internal/...")
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		from := group(fields[0])
		if from == "" {
			continue
		}
		if _, ok := allowed[from]; !ok {
			t.Errorf("internal/%s is not a declared group; add it to allowed (P1)", from)
			continue
		}
		for _, imp := range fields[1:] {
			if to := group(imp); to != "" && to != from && !slices.Contains(allowed[from], to) {
				t.Errorf("%s imports %s, an edge not declared in allowed (P1)", from, to)
			}
		}
	}
	var visit func(g string, path []string)
	visit = func(g string, path []string) {
		if slices.Contains(path, g) {
			t.Fatalf("declared groups form a cycle: %s", strings.Join(append(path, g), " → "))
		}
		for _, next := range allowed[g] {
			visit(next, append(path, g))
		}
	}
	for g := range allowed {
		visit(g, nil)
	}
}

// group returns the command group an internal package belongs to, or "" for
// shared packages and anything outside internal/.
func group(pkg string) string {
	rest, ok := strings.CutPrefix(pkg, module)
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "/")
	switch name {
	case "cli", "store", "archtest":
		return ""
	}
	return name
}
```

- [ ] **Step 2: RED** — `go test ./cmd/aits -run TestTask` fails: `unknown command "task"`. `go test ./internal/archtest -run TestGroupDependencies` passes (task already imports only shell), which is expected.

- [ ] **Step 3: `internal/cli/task.go`**

```go
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/agent-in-the-shell/aits/internal/shell"
	"github.com/agent-in-the-shell/aits/internal/task"
)

func taskGroup() *Command {
	return &Command{
		Name:     "task",
		Summary:  "Queue agent runs, work them one at a time, and read their results.",
		Children: []*Command{taskCreate(), taskList(), taskShow(), taskWork()},
	}
}

// taskDetails documents the task record printed by show and work (P5).
const taskDetails = `Task fields (--json):
  id           Created by "task create"; ids sort by creation time.
  status       queued, running, done or failed.
  agent, cwd, prompt, timeout_ms, created_at
  env          Variable names passed from the worker's environment.
  lease        While running: token, worker_pid, pgid, expires_at.
  run          Once worked: the "shell submit" result (status, session_id,
               answer, exit_code, duration_ms, stderr_tail, message) plus
               started_at and finished_at. run.status is a shell status,
               not_started (the agent could not be started), or
               lease_expired (the worker died; set by the next work or list).

Exit codes:
  0  Done, or nothing to do.
  1  The task failed, or the command could not finish.
  2  Usage error or invalid input; nothing was written.
`

func taskCreate() *Command {
	return &Command{
		Name:    "create",
		Args:    "(PROMPT | -)",
		Summary: "Queue a prompt for an agent; prints the task id.",
		Setup: func(fs *flag.FlagSet) Runner {
			agent := fs.String("agent", "", "`AGENT` CLI to run (required). One of: "+strings.Join(shell.Agents(), ", ")+".")
			cwd := nonEmpty(fs, "cwd", "Run the agent in `DIR` (default: the current directory).", "a directory")
			timeout := fs.Duration("timeout", shell.DefaultTimeout, "Stop the agent after `DURATION`, e.g. 90s or 30m.")
			var pass []string
			fs.Func("env", "Pass the variable `NAME` from the worker's environment to the agent. Repeatable.", func(v string) error {
				pass = append(pass, v)
				return nil
			})
			return func(ctx context.Context, env *Env, args []string) (int, error) {
				if len(args) != 1 {
					return 0, usageError("invalid_input", "expected one prompt argument, or - to read it from stdin; got %d", len(args))
				}
				prompt := args[0]
				if prompt == "-" {
					var err error
					if prompt, err = readPrompt(ctx, env.Stdin); err != nil {
						return 0, err
					}
				}
				dir, err := filepath.Abs(*cwd)
				if err != nil {
					return 0, failure("internal", "resolve working directory: %v", err)
				}
				t, err := task.Create(env.Environ, task.Spec{Agent: *agent, Cwd: dir, Prompt: prompt, Timeout: *timeout, Env: pass})
				if err != nil {
					return 0, taskError(err)
				}
				if env.JSON {
					writeJSON(env.Stdout, map[string]string{"id": t.ID, "status": string(t.Status)})
				} else {
					fmt.Fprintln(env.Stdout, t.ID)
				}
				return exitOK, nil
			}
		},
	}
}

func taskList() *Command {
	return &Command{
		Name:    "list",
		Summary: "List tasks, oldest first.",
		Setup: func(*flag.FlagSet) Runner {
			return func(_ context.Context, env *Env, args []string) (int, error) {
				if len(args) != 0 {
					return 0, usageError("invalid_input", "list takes no arguments")
				}
				tasks, err := task.List(env.Environ)
				if err != nil {
					return 0, taskError(err)
				}
				type item struct {
					ID        string      `json:"id"`
					Status    task.Status `json:"status"`
					Agent     string      `json:"agent"`
					Cwd       string      `json:"cwd"`
					CreatedAt time.Time   `json:"created_at"`
				}
				items := []item{}
				for _, t := range tasks {
					items = append(items, item{t.ID, t.Status, t.Agent, t.Cwd, t.CreatedAt})
				}
				if env.JSON {
					writeJSON(env.Stdout, map[string]any{"tasks": items})
					return exitOK, nil
				}
				for _, i := range items {
					fmt.Fprintf(env.Stdout, "%s  %s  %s  %s\n", i.ID, i.Status, i.Agent, i.CreatedAt.Format(time.RFC3339))
				}
				return exitOK, nil
			}
		},
	}
}

func taskShow() *Command {
	return &Command{
		Name:    "show",
		Args:    "ID",
		Summary: "Show one task and its run.",
		Details: taskDetails,
		Setup: func(*flag.FlagSet) Runner {
			return func(_ context.Context, env *Env, args []string) (int, error) {
				if len(args) != 1 {
					return 0, usageError("invalid_input", "expected one task id; got %d", len(args))
				}
				t, err := task.Show(env.Environ, args[0])
				if err != nil {
					return 0, taskError(err)
				}
				writeTask(env, t)
				return exitOK, nil
			}
		},
	}
}

func taskWork() *Command {
	return &Command{
		Name:    "work",
		Summary: "Claim the oldest queued task, run it, record the result, exit.",
		Details: taskDetails,
		Setup: func(fs *flag.FlagSet) Runner {
			agent := nonEmpty(fs, "agent", "Only claim tasks for `AGENT`.", "an agent (one of: "+strings.Join(shell.Agents(), ", ")+")")
			return func(ctx context.Context, env *Env, args []string) (int, error) {
				if len(args) != 0 {
					return 0, usageError("invalid_input", "work takes no arguments")
				}
				t, err := task.Work(ctx, env.Environ, *agent)
				if err != nil {
					return 0, taskError(err)
				}
				if t == nil {
					if env.JSON {
						writeJSON(env.Stdout, map[string]string{"status": "empty"})
					}
					return exitOK, nil
				}
				writeTask(env, *t)
				if t.Status != task.Done {
					return exitFailure, nil
				}
				return exitOK, nil
			}
		},
	}
}

// writeTask prints a task: the record with --json; otherwise a summary line,
// the session and the answer of a done run, or why it failed.
func writeTask(env *Env, t task.Task) {
	if env.JSON {
		writeJSON(env.Stdout, t)
		return
	}
	fmt.Fprintf(env.Stdout, "%s  %s  %s  %s\n", t.ID, t.Status, t.Agent, t.Cwd)
	if t.Run == nil {
		return
	}
	if t.Run.SessionID != "" {
		fmt.Fprintf(env.Stderr, "session: %s\n", t.Run.SessionID)
	}
	if t.Status == task.Done {
		fmt.Fprintln(env.Stdout, t.Run.Answer)
	} else {
		fmt.Fprintf(env.Stderr, "aits: task %s failed: %s: %s\n", t.ID, t.Run.Status, t.Run.Message)
	}
}

func taskError(err error) error {
	var invalid *task.InvalidError
	switch {
	case errors.As(err, &invalid):
		return usageError("invalid_input", "%s", invalid.Reason)
	case errors.Is(err, shell.ErrStopped):
		return failure("stopped", "%v", err)
	}
	return failure("internal", "%v", err)
}
```

`work`'s text output for a done task is the summary line then the answer on stdout and the session on stderr.

In `internal/cli/command.go` `Root`, children become `shellGroup(), sessionGroup(), taskGroup()`.

- [ ] **Step 4: GREEN** — `go test -count=1 ./cmd/aits -run TestCLIDocs -update; gofmt -l .; go vet ./... && go test -count=1 ./... && go test -count=1 -race ./internal/...` → ok.

- [ ] **Step 5: README** — under Usage, after the session search example:

```
Queue work for agents and collect it later:

    id=$(aits task create --agent claude --cwd ~/src/proj "Review internal/session for bugs")
    aits task work            # in one or more terminals; one task per call
    aits task show "$id" --json
```

and mark the `aits task` row of the planned-groups table `(available)`; update the Status line to name `aits task`.

- [ ] **Step 6: size** — `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /tmp/x ./cmd/aits` and compare with main: within budget, growth recorded in the PR.

- [ ] **Step 7: commit** `feat(cli): aits task create, list, show, work` — `internal/cli/task.go internal/cli/command.go cmd/aits/task_test.go internal/archtest/graph_test.go README.md docs/cli.md`.
