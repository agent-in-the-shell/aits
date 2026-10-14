// Package task is the "task" command group: a local queue of agent runs. A
// dispatcher creates tasks; workers claim and run one task per call through
// shell; results are read back by id (spec 2026-10-14-task-queue-design).
package task

import (
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
	tasks, groups, err := reclaim(dir)
	unlock()
	shell.Stop(groups...)
	return tasks, err
}

// Show returns one task; an unknown id is a *shell.InvalidRequestError.
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
// errReclaimed, wrapped with the task id, when the lease was lost.
func Work(ctx context.Context, environ []string, agent string) (*Task, error) {
	if agent != "" && !slices.Contains(shell.Agents(), agent) {
		return nil, invalid("unknown agent %q (one of: %s)", agent, strings.Join(shell.Agents(), ", "))
	}
	dir, err := store.Dir(environ, "task")
	if err != nil {
		return nil, err
	}
	t, err := claim(ctx, dir, agent)
	if t == nil || err != nil {
		return nil, err
	}
	token := t.Lease.Token
	started := time.Now().UTC()
	req := t.request(environ)
	req.Started = func(pgid int) {
		_, _ = update(dir, t.ID, token, func(t *Task) { t.Lease.PGID = pgid })
	}
	res, err := shell.Submit(ctx, req)
	if err != nil {
		res, started = shell.Result{Status: NotStarted, Agent: t.Agent, Message: err.Error()}, time.Time{}
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
// task of agent running with a fresh lease. A signal that arrived while it
// waited for the lock claims nothing.
func claim(ctx context.Context, dir, agent string) (*Task, error) {
	unlock, err := store.Lock(dir)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		unlock()
		return nil, shell.ErrStopped
	}
	tasks, groups, err := reclaim(dir)
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
	shell.Stop(groups...)
	return claimed, err
}

// reclaim, under the lock, loads every task and fails each running one whose
// lease has expired and whose worker is gone. It returns the tasks and the
// agent process groups to stop once the lock is free. A live worker keeps its
// task even past the lease: the lease is wall-clock time, and after the
// machine sleeps it can expire while the worker's own timeout (monotonic)
// still has time left.
func reclaim(dir string) ([]Task, []int, error) {
	tasks, err := load(dir)
	if err != nil {
		return nil, nil, err
	}
	var groups []int
	now := time.Now()
	for i := range tasks {
		t := &tasks[i]
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
			return tasks, groups, err
		}
	}
	return tasks, groups, nil
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
	t, err := read(path(dir, id))
	if err != nil {
		return Task{}, err
	}
	if t.Lease == nil || t.Lease.Token != token {
		return t, fmt.Errorf("task %s was %w", id, errReclaimed)
	}
	fn(&t)
	return t, write(dir, t)
}

// load reads every task in dir, oldest first by created_at, then by id.
// (Readdirnames rather than os.ReadDir keeps the binary smaller, P14.)
func load(dir string) ([]Task, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	names, err := f.Readdirnames(-1)
	f.Close()
	if err != nil {
		return nil, err
	}
	var tasks []Task
	for _, name := range names {
		if strings.HasSuffix(name, ".json") && !strings.HasPrefix(name, ".") {
			t, err := read(filepath.Join(dir, name))
			if err != nil {
				return nil, err
			}
			tasks = append(tasks, t)
		}
	}
	slices.SortFunc(tasks, func(a, b Task) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	return tasks, nil
}

// request is the shell request that runs t in environ.
func (t Task) request(environ []string) shell.Request {
	return shell.Request{
		Agent: t.Agent, Prompt: t.Prompt, Dir: t.Cwd, Timeout: time.Duration(t.TimeoutMS) * time.Millisecond,
		Environ: environ, Pass: t.Env,
	}
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
