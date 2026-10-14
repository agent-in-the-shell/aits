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

// load reads every task in dir, oldest first. Ids carry the creation second
// only, so tasks created within one second are ordered by created_at.
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
	slices.SortStableFunc(tasks, func(a, b Task) int { return a.CreatedAt.Compare(b.CreatedAt) })
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
