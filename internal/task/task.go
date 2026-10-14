// Package task is the "task" command group: a local queue of agent runs. A
// dispatcher creates tasks; workers claim and run one task per call through
// shell; results are read back by id (spec 2026-10-14-task-queue-design).
package task

import (
	"bytes"
	"cmp"
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
	StartedAt  time.Time `json:"started_at,omitzero"` // absent when the agent never ran
	FinishedAt time.Time `json:"finished_at"`
}

// Spec is a task to create.
type Spec struct {
	Agent, Cwd, Prompt string
	Timeout            time.Duration
	Env                []string
}

// invalid rejects input as shell does, so every invalid input of the group is
// a *shell.InvalidRequestError.
func invalid(format string, a ...any) error {
	return &shell.InvalidRequestError{Reason: fmt.Sprintf(format, a...)}
}

// errReclaimed means the worker's lease expired and another caller failed the
// task before the worker could record its run.
var errReclaimed = errors.New("reclaimed after its lease expired")

// Create validates s as the request it will be run as, and queues it.
func Create(environ []string, s Spec) (Task, error) {
	now := time.Now().UTC()
	t := Task{
		Status: Queued, Agent: s.Agent, Cwd: s.Cwd, Prompt: s.Prompt,
		TimeoutMS: s.Timeout.Milliseconds(), Env: s.Env, CreatedAt: now,
	}
	if err := t.request(nil).Validate(); err != nil {
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
	for {
		t.ID = fmt.Sprintf("%s-%06x", now.Format("20060102T150405"), rand.Uint32()&0xffffff)
		if _, err := os.Stat(path(dir, t.ID)); errors.Is(err, os.ErrNotExist) {
			break
		}
	}
	return t, write(dir, t)
}

// List reclaims expired leases and returns every readable task, oldest
// first, and the sorted ids of the task files that could not be read.
func List(environ []string) ([]Task, []string, error) {
	dir, err := store.Dir(environ, "task")
	if err != nil {
		return nil, nil, err
	}
	unlock, err := store.Lock(dir)
	if err != nil {
		return nil, nil, err
	}
	s, groups, err := reclaim(dir)
	unlock()
	shell.Stop(groups...)
	return s.tasks, s.skipped, err
}

// Show returns one task; an unknown id is a *shell.InvalidRequestError.
func Show(environ []string, id string) (Task, error) {
	dir, err := store.Dir(environ, "task")
	if err != nil {
		return Task{}, err
	}
	t, err := read(dir, id)
	if errors.Is(err, os.ErrNotExist) {
		return Task{}, invalid("no task %s", id)
	}
	return t, err
}

// Work reclaims expired leases, claims the oldest queued task (of agent, if
// not empty), runs it through shell and records the run. It also returns the
// ids of the task files it skipped as unreadable. The task is nil when
// nothing is queued; the error is shell.ErrStopped when ctx is done before
// the claim, and errReclaimed, wrapped with the task id, when the lease was
// lost.
func Work(ctx context.Context, environ []string, agent string) (*Task, []string, error) {
	if agent != "" && !slices.Contains(shell.Agents(), agent) {
		return nil, nil, invalid("unknown agent %q (one of: %s)", agent, strings.Join(shell.Agents(), ", "))
	}
	dir, err := store.Dir(environ, "task")
	if err != nil {
		return nil, nil, err
	}
	t, skipped, err := claim(ctx, dir, agent)
	if t == nil || err != nil {
		return nil, skipped, err
	}
	token := t.Lease.Token
	var started time.Time // stays zero unless the agent starts
	req := t.request(environ)
	req.Started = func(pgid int) {
		started = time.Now().UTC()
		_, _ = update(dir, t.ID, token, func(t *Task) { t.Lease.PGID = pgid })
	}
	res, err := shell.Submit(ctx, req)
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
		return nil, skipped, err
	}
	return &done, skipped, nil
}

// claim, under the lock, reclaims expired leases and marks the oldest queued
// task of agent running with a fresh lease; it also returns the ids of the
// unreadable task files. A signal that arrived while it waited for the lock
// claims nothing.
func claim(ctx context.Context, dir, agent string) (*Task, []string, error) {
	unlock, err := store.Lock(dir)
	if err != nil {
		return nil, nil, err
	}
	if ctx.Err() != nil {
		unlock()
		return nil, nil, shell.ErrStopped
	}
	s, groups, err := reclaim(dir)
	var claimed *Task
	if err == nil {
		for i := range s.tasks {
			if t := &s.tasks[i]; t.Status == Queued && (agent == "" || t.Agent == agent) {
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
	shell.Stop(groups...)
	return claimed, s.skipped, err
}

// reclaim, under the lock, loads every task and fails each running one whose
// lease has expired and whose worker is gone. It returns what it loaded and
// the agent process groups to stop once the lock is free. A live worker keeps its
// task even past the lease: the lease is wall-clock time, and after the
// machine sleeps it can expire while the worker's own timeout (monotonic)
// still has time left.
func reclaim(dir string) (snapshot, []int, error) {
	s, err := load(dir)
	if err != nil {
		return s, nil, err
	}
	var groups []int
	now := time.Now()
	for i := range s.tasks {
		t := &s.tasks[i]
		if t.Status != Running || t.Lease == nil || now.Before(t.Lease.ExpiresAt) || alive(t.Lease.WorkerPID) {
			continue
		}
		groups = append(groups, t.Lease.PGID)
		t.Status, t.Lease = Failed, nil
		t.Run = &Run{Result: shell.Result{
			Status: LeaseExpired, Agent: t.Agent,
			Message: "the worker's lease expired before it recorded a result",
		}, FinishedAt: now.UTC()}
		if err := write(dir, *t); err != nil {
			return s, groups, err
		}
	}
	return s, groups, nil
}

// alive reports whether process pid exists.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return pid > 0 && (err == nil || errors.Is(err, syscall.EPERM))
}

// update applies fn to task id under the lock if the caller still holds its
// lease (token), and writes it back.
func update(dir, id, token string, fn func(*Task)) (Task, error) {
	unlock, err := store.Lock(dir)
	if err != nil {
		return Task{}, err
	}
	defer unlock()
	t, err := read(dir, id)
	if err != nil {
		return Task{}, err
	}
	if t.Lease == nil || t.Lease.Token != token {
		return t, fmt.Errorf("task %s was %w", id, errReclaimed)
	}
	fn(&t)
	return t, write(dir, t)
}

// snapshot is the task directory as load found it.
type snapshot struct {
	tasks   []Task   // every readable task, oldest first by created_at, then by id
	skipped []string // the sorted ids of task files that could not be read
}

// load reads every task in dir. An unreadable task file is skipped and left
// as it is (P10); a directory that cannot be listed is an error.
// (Readdirnames rather than os.ReadDir keeps the binary smaller, P14.)
func load(dir string) (snapshot, error) {
	var s snapshot
	f, err := os.Open(dir)
	if err != nil {
		return s, err
	}
	names, err := f.Readdirnames(-1)
	f.Close()
	if err != nil {
		return s, err
	}
	for _, name := range names {
		id, ok := strings.CutSuffix(name, ".json")
		if !ok || strings.HasPrefix(name, ".") {
			continue
		}
		if t, err := read(dir, id); err == nil {
			s.tasks = append(s.tasks, t)
		} else {
			s.skipped = append(s.skipped, id)
		}
	}
	slices.SortFunc(s.tasks, func(a, b Task) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	slices.Sort(s.skipped)
	return s, nil
}

// request is the shell request that runs t in environ.
func (t Task) request(environ []string) shell.Request {
	return shell.Request{
		Agent: t.Agent, Prompt: t.Prompt, Dir: t.Cwd, Timeout: time.Duration(t.TimeoutMS) * time.Millisecond,
		Environ: environ, Pass: t.Env,
	}
}

// validID reports whether id is a task id as Create makes them,
// YYYYMMDDTHHMMSS-xxxxxx with lower-case hex: the only names read opens, so
// that a case-insensitive file system cannot open a task by another spelling.
func validID(id string) bool {
	return len(id) == 22 && id[8] == 'T' && id[15] == '-' &&
		strings.Trim(id[:8]+id[9:15], "0123456789") == "" &&
		strings.Trim(id[16:], "0123456789abcdef") == ""
}

func path(dir, id string) string { return filepath.Join(dir, id+".json") }

// read reads task id in dir, failing with "task ID could not be read". A
// task is readable only as a regular file (not a symlink, FIFO or device:
// read never blocks, P10) named by a task id and holding the record of that
// id, with a known status and a lease while running (P6); anything else,
// such as a copy of another task's file, is an error. An id that is not a
// task id is os.ErrNotExist, so no other spelling opens a task.
func read(dir, id string) (Task, error) {
	t, err := readFile(dir, id)
	if err != nil {
		return Task{}, fmt.Errorf("task %s could not be read: %w", id, err)
	}
	return t, nil
}

func readFile(dir, id string) (Task, error) {
	if !validID(id) {
		return Task{}, fmt.Errorf("not a task id: %w", os.ErrNotExist)
	}
	f, err := os.OpenFile(path(dir, id), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Task{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Task{}, err
	}
	if !fi.Mode().IsRegular() {
		return Task{}, errors.New("not a regular file")
	}
	var b bytes.Buffer
	b.Grow(int(fi.Size()) + 1)
	if _, err := b.ReadFrom(f); err != nil {
		return Task{}, err
	}
	var t Task
	if err := json.Unmarshal(b.Bytes(), &t); err != nil {
		return Task{}, err
	}
	switch {
	case t.ID != id:
		return Task{}, fmt.Errorf("its id is %q, not %q", t.ID, id)
	case !slices.Contains([]Status{Queued, Running, Done, Failed}, t.Status):
		return Task{}, fmt.Errorf("unknown status %q", t.Status)
	case t.Status == Running && t.Lease == nil:
		return Task{}, errors.New("running without a lease")
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
