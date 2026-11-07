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
	"time"
	"unicode/utf8"

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

// Statuses are the statuses a task can have, in the order of its life.
func Statuses() []Status { return []Status{Queued, Running, Done, Failed} }

// Run statuses besides shell's: the agent was never started after the claim,
// the worker exited before it recorded a result, or task cancel withdrew the
// task before any worker claimed it (#126).
const (
	NotStarted shell.Status = "not_started"
	WorkerDied shell.Status = "worker_died"
	Cancelled  shell.Status = "cancelled"
)

// Task is one queued agent run and, once worked, its result.
type Task struct {
	ID        string   `json:"id"`
	Status    Status   `json:"status"`
	Agent     string   `json:"agent"`
	Cwd       string   `json:"cwd"`
	Prompt    string   `json:"prompt"`
	TimeoutMS int64    `json:"timeout_ms"`
	Env       []string `json:"env,omitempty"`   // names only (P8)
	After     string   `json:"after,omitempty"` // the task whose session this one continues (#63)
	// Session is the session this task continues, given at create (#123);
	// a follow-up's is its parent's, read when it is claimed.
	Session string `json:"session,omitempty"`
	Model   string `json:"model,omitempty"` // empty: the vendor's default (#119)
	// Label groups tasks, such as the runs of one scheduled runbook, for
	// list and work (#131); a follow-up's is its parent's unless given.
	Label string `json:"label,omitempty"`
	Write bool   `json:"write,omitempty"` // set by create --write only, for this task alone (P9, #104)
	// AllowTools is set by create --allow-tool only, for this task alone
	// (P9, #121).
	AllowTools []string  `json:"allow_tools,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	Lease      *Lease    `json:"lease"`
	Run        *Run      `json:"run"`
}

// Lease is a worker's claim on a running task. Whether the worker is alive is
// not recorded: it holds the task's lock file while it runs (lockPath).
type Lease struct {
	Token     string    `json:"token"`
	PGID      int       `json:"pgid"`                // the agent's process group; 0 until it starts
	StartedAt time.Time `json:"started_at,omitzero"` // when the agent started, recorded with PGID
	// ActiveAt is when the agent last printed output, read from the task's
	// beat file (beatPath) as the task is read; never stored (#151).
	ActiveAt time.Time `json:"active_at,omitzero"`
}

// Run is the outcome of working a task.
type Run struct {
	shell.Result
	StartedAt  time.Time `json:"started_at,omitzero"` // absent when the agent never ran
	FinishedAt time.Time `json:"finished_at"`
}

// Spec is a task to create. With After set, an empty Agent or Cwd is taken
// from that task. Session and After exclude each other.
type Spec struct {
	Agent, Cwd, Prompt, After string
	Session                   string // a session to continue (#123)
	Model                     string // with After, empty is that task's model
	Label                     string // with After, empty is that task's label (#131)
	Timeout                   time.Duration
	Env                       []string
	Write                     bool     // never taken from After (#104)
	AllowTools                []string // never taken from After (#121)
}

// invalid rejects input as shell does, so every invalid input of the group is
// a *shell.InvalidRequestError.
func invalid(format string, a ...any) error {
	return &shell.InvalidRequestError{Reason: fmt.Sprintf(format, a...)}
}

// taskDir is the queue's directory. A root that cannot be found is the
// caller's invalid input, as session search reports its own (P5).
func taskDir(environ []string) (string, error) {
	dir, err := store.Dir(environ, "task")
	if errors.Is(err, store.ErrNoRoot) {
		return "", invalid("%v", err)
	}
	return dir, err
}

// errReclaimed means another caller failed the task, its worker being gone,
// before this worker recorded its run.
var errReclaimed = errors.New("reclaimed before its run was recorded")

// Notes are what list and work report besides tasks, as sorted ids: task
// files that could not be read, and running tasks whose lock file is missing,
// so that whether their worker is alive cannot be told.
type Notes struct{ Skipped, Unknown []string }

// lockPath is the lock file a task's worker holds while it runs. It is made
// by the claim and never removed: removing a held lock file would let a new
// one at the same path be locked by someone else.
func lockPath(dir, id string) string { return filepath.Join(dir, id+".lock") }

// stopPath is the stop request Cancel leaves for the worker of a running
// task (#149): a file of its own, since the worker rewrites its record
// without the directory lock. The worker removes it once it records the run.
func stopPath(dir, id string) string { return filepath.Join(dir, id+".stop") }

// beatPath is the file whose modification time says when a running task's
// agent last printed output (#151). Its own file, as stopPath, so the record
// is not rewritten for it.
func beatPath(dir, id string) string { return filepath.Join(dir, id+".beat") }

// beatEvery bounds how often a worker marks its task active.
const beatEvery = 5 * time.Second

// stopMessage is the run message of a task Cancel stopped.
const stopMessage = "stopped by task cancel"

// Create validates s as the request it will be run as, and queues it. A
// follow-up (s.After) is checked against its parent under the lock.
func Create(environ []string, s Spec) (Task, error) {
	now := time.Now().UTC()
	t := Task{
		Status: Queued, Agent: s.Agent, Cwd: s.Cwd, Prompt: s.Prompt,
		TimeoutMS: s.Timeout.Milliseconds(), Env: s.Env, After: s.After, Session: s.Session, Model: s.Model, Label: s.Label, Write: s.Write, AllowTools: s.AllowTools, CreatedAt: now,
	}
	if s.Session != "" && s.After != "" {
		return Task{}, invalid("a task continues either a session or a task, not both: a follow-up continues its parent's session")
	}
	if s.After == "" {
		if err := t.validate(s.Timeout); err != nil {
			return Task{}, err
		}
	}
	dir, err := taskDir(environ)
	if err != nil {
		return Task{}, err
	}
	unlock, err := store.Lock(dir)
	if err != nil {
		return Task{}, err
	}
	defer unlock()
	if s.After != "" {
		if err := t.follow(dir, s.Timeout); err != nil {
			return Task{}, err
		}
	}
	if s.Session != "" {
		if err := sessionFree(dir, s.Session); err != nil {
			return Task{}, err
		}
	}
	for {
		t.ID = fmt.Sprintf("%s-%06x", now.Format("20060102T150405"), rand.Uint32()&0xffffff)
		_, err := os.Stat(path(dir, t.ID))
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil { // not a collision: another id would fail the same way (P6)
			return Task{}, fmt.Errorf("a new task could not be named: %w", err)
		}
	}
	if err := write(dir, t); err != nil {
		return Task{}, err
	}
	_ = store.Rotate(eventsPath(dir), eventsMax, eventsKeep) // under the lock, so no other writer rotates
	emit(dir, EventCreated, t)
	return t, nil
}

// validate checks t as the request it will be run as, with timeout as given
// rather than its stored form (P6). The record is JSON text, which would
// replace bytes that are not UTF-8 with U+FFFD, so they are refused.
func (t Task) validate(timeout time.Duration) error {
	if !utf8.ValidString(t.Prompt) {
		return invalid("the prompt is not valid UTF-8")
	}
	if !utf8.ValidString(t.Cwd) {
		return invalid("working directory %q is not valid UTF-8", t.Cwd)
	}
	if !utf8.ValidString(t.Model) {
		return invalid("model %q is not valid UTF-8", t.Model)
	}
	if t.Label != "" && !ValidLabel(t.Label) {
		return invalid("label %q %s", t.Label, labelRule)
	}
	for _, name := range t.Env {
		if !utf8.ValidString(name) {
			return invalid("environment variable name %q is not valid UTF-8", name)
		}
	}
	r := t.request(nil)
	r.Timeout = timeout
	if err := r.Validate(); err != nil {
		return err
	}
	if timeout%time.Millisecond != 0 {
		return invalid("a task's timeout must be a whole number of milliseconds, got %s", timeout)
	}
	return nil
}

// follow makes t a follow-up of task t.After, under the directory lock: the
// parent must exist and not have failed or ended without a session, t takes
// its agent and cwd, and the parent may have no other follow-up that is not
// failed (#63).
func (t *Task) follow(dir string, timeout time.Duration) error {
	p, err := find(dir, t.After)
	if err != nil {
		return err
	}
	if why := parentFault(&p, false); why != "" {
		return invalid("task %s %s; only a done task with a session can be followed up", p.ID, why)
	}
	switch {
	case t.Agent != "" && t.Agent != p.Agent:
		return invalid("agent %s is not task %s's agent %s", t.Agent, p.ID, p.Agent)
	case t.Cwd != "" && t.Cwd != p.Cwd:
		return invalid("working directory %q is not task %s's %q", t.Cwd, p.ID, p.Cwd)
	}
	t.Agent, t.Cwd = p.Agent, p.Cwd
	if t.Model == "" {
		t.Model = p.Model
	}
	if t.Label == "" {
		t.Label = p.Label
	}
	if err := t.validate(timeout); err != nil {
		return err
	}
	s, err := load(dir)
	if err != nil {
		return err
	}
	if len(s.Skipped) > 0 {
		return fmt.Errorf("cannot tell whether task %s already has a follow-up: task %s could not be read", p.ID, s.Skipped[0])
	}
	for _, o := range s.tasks {
		if o.After != p.ID {
			continue
		}
		if o.Status != Failed {
			return invalid("task %s already has a follow-up: %s; follow up on %s instead", p.ID, o.ID, o.ID)
		}
		if busy, err := stopping(dir, o); err != nil || busy {
			if err == nil {
				err = invalid("task %s, a follow-up of %s, failed but its agent is still being stopped; try again in a few seconds", o.ID, p.ID)
			}
			return err
		}
	}
	if p.Status != Done {
		return nil // its session is not known yet
	}
	return sessionFree(dir, p.Run.SessionID)
}

// stopping reports whether failed task o's agent may still be running: its
// worker died, and the reclaim that failed it holds its lock until it has
// stopped the agent.
func stopping(dir string, o Task) (bool, error) {
	if o.Run == nil || o.Run.Status != WorkerDied {
		return false, nil // its agent had ended, or never ran, before it was recorded failed
	}
	release, err := store.TryLock(lockPath(dir, o.ID), false)
	switch {
	case errors.Is(err, store.ErrLocked):
		return true, nil
	case err == nil:
		release()
	case !errors.Is(err, os.ErrNotExist): // (no lock file: removed by hand, nothing to wait for)
		return false, fmt.Errorf("task %s: %w", o.ID, err)
	}
	return false, nil
}

// sessionFree checks, under the directory lock, that no task in the queue
// may still write session id: one queued or running that continues it (by
// --session, or as a follow-up of a task that ended in it), or a failed one
// whose agent is still being stopped. Two agents continuing one conversation
// at once leave it undefined (#123, as for follow-ups, #63).
func sessionFree(dir, id string) error {
	s, err := load(dir)
	if err != nil {
		return err
	}
	if len(s.Skipped) > 0 {
		return fmt.Errorf("cannot tell whether session %s is already being continued: task %s could not be read", id, s.Skipped[0])
	}
	byID := index(s.tasks)
	for _, o := range s.tasks {
		if continues(o, byID) != id {
			continue
		}
		switch busy, err := stopping(dir, o); {
		case err != nil:
			return err
		case o.Status == Queued || o.Status == Running:
			return invalid("session %s is already continued by task %s; follow up on %s instead", id, o.ID, o.ID)
		case busy:
			return invalid("task %s, which continued session %s, failed but its agent is still being stopped; try again in a few seconds", o.ID, id)
		}
	}
	return nil
}

// continues is the session task t continues, as far as the queue knows: its
// own, or a follow-up's parent's once the parent is done ("" before).
func continues(t Task, byID map[string]*Task) string {
	if t.After == "" {
		return t.Session
	}
	if p := byID[t.After]; p != nil && p.Status == Done && p.Run != nil {
		return p.Run.SessionID
	}
	return ""
}

// List reclaims tasks of dead workers and returns every readable task, oldest
// first, and its notes.
func List(environ []string) ([]Task, Notes, error) {
	dir, err := taskDir(environ)
	if err != nil {
		return nil, Notes{}, err
	}
	unlock, err := store.Lock(dir)
	if err != nil {
		return nil, Notes{}, err
	}
	s, stop, err := reclaim(dir)
	unlock()
	stop()
	return s.tasks, s.Notes, err
}

// Cancel withdraws task id (#126, #149). A queued task becomes failed with
// run status cancelled, under the lock that claim takes, so a cancel and a
// claim never both succeed; a queued follow-up of it then fails at once
// (parentFault). A running task gets a stop request (stopPath), which its
// worker acts on by stopping the agent as a signal does; Cancel then waits
// for the worker to record the run, as Wait does, with Wait's timeout and
// unknown. It reclaims as List does first, so the task of a dead worker is
// returned failed (worker_died) with no request. A done or failed task is
// refused unchanged.
func Cancel(ctx context.Context, environ []string, id string, timeout time.Duration, unknown func()) (Task, error) {
	if timeout < 0 {
		return Task{}, invalid("timeout must not be negative, got %s", timeout)
	}
	dir, err := taskDir(environ)
	if err != nil {
		return Task{}, err
	}
	t, stopping, err := withdraw(dir, id)
	if err != nil || !stopping {
		return t, err
	}
	t, err = Wait(ctx, environ, id, timeout, unknown)
	if err == nil {
		_ = store.Remove(stopPath(dir, id)) // a request that came after the worker had recorded its run
	}
	return t, err
}

// withdraw is Cancel under the lock: it cancels a queued task, or leaves a
// stop request for a running one and reports that it did.
func withdraw(dir, id string) (Task, bool, error) {
	unlock, err := store.Lock(dir)
	if err != nil {
		return Task{}, false, err
	}
	s, stop, err := reclaim(dir)
	defer stop()
	defer unlock() // runs before stop: stop waits for dead workers' agents
	if err != nil {
		return Task{}, false, err
	}
	i := slices.IndexFunc(s.tasks, func(t Task) bool { return t.ID == id })
	if i < 0 {
		if _, err := find(dir, id); err != nil {
			return Task{}, false, err // no such task, or one that cannot be read
		}
		return Task{}, false, fmt.Errorf("task %s could not be read", id)
	}
	t := &s.tasks[i]
	switch {
	case t.Status == Running:
		if err := store.WriteFile(stopPath(dir, id), nil); err != nil {
			return Task{}, false, fmt.Errorf("task %s: the stop request could not be written: %w", id, err)
		}
		return *t, true, nil
	case slices.Contains(s.reclaimed, id):
		return *t, false, nil // its worker is gone: nothing to stop
	case t.Status != Queued:
		return Task{}, false, invalid("task %s is %s; only a queued or running task can be cancelled", id, t.Status)
	}
	t.Status = Failed
	t.Run = &Run{Result: shell.Result{
		Status: Cancelled, Agent: t.Agent, Message: "withdrawn by task cancel before a worker claimed it",
	}, FinishedAt: time.Now().UTC()}
	if err := write(dir, *t); err != nil {
		return Task{}, false, err
	}
	emit(dir, EventCancelled, *t)
	done := *t
	return done, false, failOrphans(dir, s)
}

// Show returns one task (see find).
func Show(environ []string, id string) (Task, error) {
	dir, err := taskDir(environ)
	if err != nil {
		return Task{}, err
	}
	return find(dir, id)
}

// pollEvery is how often Wait looks at the queue again.
const pollEvery = 250 * time.Millisecond

// TimeoutError means Wait's timeout elapsed with the task not yet done or
// failed; Task is the task as last read.
type TimeoutError struct {
	Task    Task
	Timeout time.Duration
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("task %s is still %s after %s", e.Task.ID, e.Task.Status, e.Timeout)
}

// Wait returns task id once it is done or failed (#110). Each look reclaims
// as List does, so the task of a dead worker, or a follow-up that can never
// run, ends instead of being waited for; Wait never takes a task's lock
// itself. unknown is called once if the task is running and its lock file is
// missing, so that whether its worker is alive cannot be told. With a timeout
// above 0, a task still queued or running after it is a *TimeoutError; when
// ctx is done first, the error is ctx's.
func Wait(ctx context.Context, environ []string, id string, timeout time.Duration, unknown func()) (Task, error) {
	if timeout < 0 {
		return Task{}, invalid("timeout must not be negative, got %s", timeout)
	}
	dir, err := taskDir(environ)
	if err != nil {
		return Task{}, err
	}
	var deadline <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		deadline = timer.C
	}
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	noted := false
	for {
		t, unk, err := look(dir, id)
		if err != nil {
			return Task{}, err
		}
		if t.Status == Done || t.Status == Failed {
			return t, nil
		}
		if unk && !noted {
			unknown()
			noted = true
		}
		select {
		case <-ctx.Done():
			return Task{}, ctx.Err()
		case <-deadline:
			return Task{}, &TimeoutError{Task: t, Timeout: timeout}
		case <-tick.C:
		}
	}
}

// maxWait bounds the ids one WaitMany takes.
const maxWait = 256

// Waited is where the tasks of a WaitMany stand: those that ended, in the
// order they ended, and those still queued or running, in the order given.
type Waited struct {
	Ended   []Task
	Pending []Task
}

// ErrWaitTimeout means WaitMany's timeout elapsed before its condition held.
var ErrWaitTimeout = errors.New("the timeout elapsed")

// WaitMany waits for tasks ids as Wait waits for one: until all of them are
// done or failed, or with anyOne until one is (#147). It returns where they
// stand even with an error: ErrWaitTimeout after timeout, or ctx's error.
// unknown is called once for each running task whose lock file is missing.
func WaitMany(ctx context.Context, environ []string, ids []string, anyOne bool, timeout time.Duration, unknown func(id string)) (Waited, error) {
	switch {
	case timeout < 0:
		return Waited{}, invalid("timeout must not be negative, got %s", timeout)
	case len(ids) == 0:
		return Waited{}, invalid("no task id to wait for")
	case len(ids) > maxWait:
		return Waited{}, invalid("at most %d task ids, got %d", maxWait, len(ids))
	}
	for i, id := range ids {
		if slices.Contains(ids[:i], id) {
			return Waited{}, invalid("task %s is given more than once", id)
		}
	}
	dir, err := taskDir(environ)
	if err != nil {
		return Waited{}, err
	}
	var deadline <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		deadline = timer.C
	}
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	var w Waited
	ended := map[string]bool{}
	noted := map[string]bool{}
	for {
		unlock, err := store.Lock(dir)
		if err != nil {
			return w, err
		}
		s, stop, err := reclaim(dir)
		unlock()
		stop()
		if err != nil {
			return w, err
		}
		var fresh []Task
		w.Pending = nil
		for _, id := range ids {
			if ended[id] {
				continue
			}
			t, err := find(dir, id)
			if err != nil {
				return w, err
			}
			if t.Status == Done || t.Status == Failed {
				fresh = append(fresh, t)
				ended[id] = true
				continue
			}
			w.Pending = append(w.Pending, t)
			if slices.Contains(s.Unknown, id) && !noted[id] {
				unknown(id)
				noted[id] = true
			}
		}
		finished := func(t Task) time.Time {
			if t.Run == nil {
				return time.Time{}
			}
			return t.Run.FinishedAt
		}
		slices.SortStableFunc(fresh, func(a, b Task) int { return finished(a).Compare(finished(b)) })
		w.Ended = append(w.Ended, fresh...)
		if len(w.Pending) == 0 || anyOne && len(w.Ended) > 0 {
			return w, nil
		}
		select {
		case <-ctx.Done():
			return w, ctx.Err()
		case <-deadline:
			return w, ErrWaitTimeout
		case <-tick.C:
		}
	}
}

// look reclaims under the lock, then reads task id, and reports whether it is
// a running task whose lock file is missing.
func look(dir, id string) (Task, bool, error) {
	unlock, err := store.Lock(dir)
	if err != nil {
		return Task{}, false, err
	}
	s, stop, err := reclaim(dir)
	unlock()
	stop()
	if err != nil {
		return Task{}, false, err
	}
	t, err := find(dir, id)
	return t, slices.Contains(s.Unknown, id), err
}

// find reads task id in dir; an unknown id is a *shell.InvalidRequestError.
func find(dir, id string) (Task, error) {
	t, err := read(dir, id)
	if errors.Is(err, os.ErrNotExist) {
		return Task{}, invalid("no task %s", id)
	}
	return t, err
}

// Work reclaims tasks of dead workers, claims the oldest claimable task (of
// agent, if not empty), runs it through shell and records the run. It also
// returns the notes of the reclaim. With label, only tasks with that label
// are claimed (#131). With maxRunning above 0, nothing is claimed while that
// many tasks (of label, if set) are running (#155). The task is nil when
// nothing is claimable;
// the error is shell.ErrStopped when ctx is done before the claim, and
// errReclaimed, wrapped with the task id, when the task was reclaimed.
func Work(ctx context.Context, kill <-chan struct{}, environ []string, agent, label string, maxRunning int) (*Task, Notes, error) {
	if agent != "" && !slices.Contains(shell.Agents(), agent) {
		return nil, Notes{}, invalid("unknown agent %q (one of: %s)", agent, strings.Join(shell.Agents(), ", "))
	}
	if label != "" && !ValidLabel(label) {
		return nil, Notes{}, invalid("label %q %s", label, labelRule)
	}
	if maxRunning < 0 {
		return nil, Notes{}, invalid("max running must not be negative, got %d", maxRunning)
	}
	dir, err := taskDir(environ)
	if err != nil {
		return nil, Notes{}, err
	}
	t, session, release, notes, err := claim(ctx, dir, agent, label, maxRunning)
	if t == nil || err != nil {
		return nil, notes, err
	}
	defer release() // the worker holds the task's lock until its run is recorded
	token := t.Lease.Token
	var started time.Time // stays zero unless the agent starts
	req := t.request(environ)
	req.Session = session
	req.Kill = kill
	var lastBeat time.Time
	beat := func() {
		if now := time.Now(); now.Sub(lastBeat) >= beatEvery {
			lastBeat = now
			_ = store.WriteFile(beatPath(dir, t.ID), nil) // a hint: a failed write leaves the last one
		}
	}
	req.Activity = beat
	req.Started = func(pgid int) error {
		started = time.Now().UTC()
		beat()
		// Not update: waiting for the directory lock would leave a window, as
		// long as another command holds it, in which a killed worker's agent
		// is unrecorded and never stopped (P10). As holder of the task's lock
		// the worker is the only writer of its running record.
		cur, err := modify(dir, t.ID, token, func(t *Task) { t.Lease.PGID, t.Lease.StartedAt = pgid, started })
		if err != nil {
			return fmt.Errorf("the agent's process group could not be recorded: %w", err)
		}
		emit(dir, EventStarted, cur)
		return nil
	}
	// A stop request ends the agent as a signal does: cancelling runCtx makes
	// Submit stop its group (TERM, grace, KILL).
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	requested := make(chan struct{})
	go func() {
		tick := time.NewTicker(pollEvery)
		defer tick.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-tick.C:
				if _, err := os.Lstat(stopPath(dir, t.ID)); err == nil {
					close(requested)
					cancel()
					return
				}
			}
		}
	}()
	res, err := shell.Submit(runCtx, req)
	if err != nil {
		res = shell.Result{Status: NotStarted, Agent: t.Agent, Message: err.Error()}
	}
	select {
	case <-requested:
		if res.Status == shell.StatusStopped && ctx.Err() == nil {
			res.Message = stopMessage
		}
	default:
	}
	status := Failed
	if res.Status == shell.StatusOK {
		status = Done
	}
	run := &Run{Result: res, StartedAt: started, FinishedAt: time.Now().UTC()}
	done, err := update(dir, t.ID, token, func(t *Task) { t.Status, t.Run, t.Lease = status, run, nil })
	if err != nil {
		return nil, notes, err
	}
	_ = store.Remove(stopPath(dir, t.ID)) // most runs have none
	_ = store.Remove(beatPath(dir, t.ID))
	emit(dir, EventEnded, done)
	return &done, notes, nil
}

// claim, under the lock, reclaims tasks of dead workers and marks the oldest
// claimable queued task of agent and label running with a fresh lease. It locks the
// task's lock file first, so that a running task's lock is never free while
// its worker lives, and returns the session the task continues ("" for a new
// one), the function that releases the lock, and the notes of the reclaim. A
// signal that arrived while it waited for the lock claims nothing.
func claim(ctx context.Context, dir, agent, label string, maxRunning int) (*Task, string, func(), Notes, error) {
	unlock, err := store.Lock(dir)
	if err != nil {
		return nil, "", nil, Notes{}, err
	}
	if ctx.Err() != nil {
		unlock()
		return nil, "", nil, Notes{}, shell.ErrStopped
	}
	s, stop, err := reclaim(dir)
	byID := index(s.tasks)
	var claimed *Task
	var session string
	var release func()
	i := slices.IndexFunc(s.tasks, func(t Task) bool {
		return t.Status == Queued && (agent == "" || t.Agent == agent) && (label == "" || t.Label == label) && claimable(t, byID)
	})
	if maxRunning > 0 { // #155: running tasks of the label (or all) count against the cap
		running := 0
		for _, t := range s.tasks {
			if t.Status == Running && (label == "" || t.Label == label) {
				running++
			}
		}
		if running >= maxRunning {
			i = -1
		}
	}
	if err == nil && i >= 0 {
		t := &s.tasks[i]
		session = t.Session
		if t.After != "" {
			session = byID[t.After].Run.SessionID // claimable checked it
		}
		if release, err = store.TryLock(lockPath(dir, t.ID), true); err != nil {
			err = fmt.Errorf("task %s: %w", t.ID, err)
		} else {
			t.Status, t.Lease = Running, &Lease{Token: fmt.Sprintf("%016x", rand.Uint64())}
			if err = write(dir, *t); err != nil {
				release()
			} else {
				claimed = t
				emit(dir, EventClaimed, *t)
			}
		}
	}
	unlock()
	stop()
	return claimed, session, release, s.Notes, err
}

// reclaim, under the lock, loads every task and fails each running one whose
// lock file it can lock: no worker holds it, so the worker is gone. A held
// lock is a live worker, whatever its age (P10); a missing lock file cannot be
// judged and is noted (P6); any other error stops it. It then fails each
// queued follow-up that can never run (parentFault). It returns what it
// loaded and stop, for the caller to run once the directory lock is free:
// stop ends the dead workers' agent groups, then releases their tasks' locks.
func reclaim(dir string) (snapshot, func(), error) {
	var groups []int
	var locks []func()
	stop := func() {
		shell.Stop(groups...)
		for _, release := range locks {
			release()
		}
	}
	s, err := load(dir)
	if err != nil {
		return s, stop, err
	}
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
			return s, stop, fmt.Errorf("task %s: %w", t.ID, err)
		}
		// Re-read: the worker writes its pgid without the directory lock, so it
		// may have done so since load. (The error branch has no test: a write
		// between load and here cannot be forced without a test seam, P2.)
		cur, err := read(dir, t.ID)
		if err != nil {
			release()
			return s, stop, err
		}
		if *t = cur; t.Status != Running {
			release()
			continue
		}
		t.Run = &Run{Result: shell.Result{
			Status: WorkerDied, Agent: t.Agent,
			Message: "the worker exited before it recorded a result",
		}, StartedAt: t.Lease.StartedAt, FinishedAt: time.Now().UTC()}
		pgid := t.Lease.PGID
		t.Status, t.Lease = Failed, nil
		if err := write(dir, *t); err != nil {
			release()
			return s, stop, err
		}
		emit(dir, EventEnded, *t)
		_ = store.Remove(beatPath(dir, t.ID))
		s.reclaimed = append(s.reclaimed, t.ID)
		groups, locks = append(groups, pgid), append(locks, release)
	}
	if err := failOrphans(dir, s); err != nil {
		return s, stop, err
	}
	slices.Sort(s.Unknown)
	return s, stop, nil
}

// failOrphans fails each queued follow-up in s that can never run
// (parentFault), until a pass changes nothing, so a whole chain fails at once
// whatever its created_at order.
func failOrphans(dir string, s snapshot) error {
	byID := index(s.tasks)
	for changed := true; changed; {
		changed = false
		for i := range s.tasks {
			t := &s.tasks[i]
			if t.Status != Queued || t.After == "" {
				continue
			}
			why := parentFault(byID[t.After], slices.Contains(s.Skipped, t.After))
			if why == "" {
				continue
			}
			t.Status = Failed
			t.Run = &Run{Result: shell.Result{
				Status: NotStarted, Agent: t.Agent, Message: "its parent task " + t.After + " " + why,
			}, FinishedAt: time.Now().UTC()}
			if err := write(dir, *t); err != nil {
				return err
			}
			emit(dir, EventEnded, *t)
			changed = true
		}
	}
	return nil
}

// index maps each task's id to it.
func index(tasks []Task) map[string]*Task {
	m := make(map[string]*Task, len(tasks))
	for i := range tasks {
		m[tasks[i].ID] = &tasks[i]
	}
	return m
}

// claimable reports whether queued task t may run now: a follow-up only once
// its parent is done with a session to continue (parentFault).
func claimable(t Task, byID map[string]*Task) bool {
	p := byID[t.After]
	return t.After == "" || (p != nil && p.Status == Done && parentFault(p, false) == "")
}

// parentFault says why a follow-up of p can never run, or "" if it may yet:
// p failed, its file is gone, or it is done without a session. A parent whose
// file cannot be read (skipped) is waited for (P10). cmd/aits/docs_test.go
// reads the reasons from here: each case starts with p and returns one string
// literal.
func parentFault(p *Task, skipped bool) string {
	switch {
	case p == nil && skipped:
		return ""
	case p == nil:
		return "does not exist"
	case p.Status == Failed:
		return "failed"
	case p.Status == Done && (p.Run == nil || p.Run.SessionID == ""):
		return "has no session"
	}
	return ""
}

// update applies fn to task id under the directory lock if the caller still
// holds its lease (token), and writes it back.
func update(dir, id, token string, fn func(*Task)) (Task, error) {
	unlock, err := store.Lock(dir)
	if err != nil {
		return Task{}, err
	}
	defer unlock()
	return modify(dir, id, token, fn)
}

// modify is update without the directory lock, for a worker writing its own
// running record while it holds the task's lock.
func modify(dir, id, token string, fn func(*Task)) (Task, error) {
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
	tasks     []Task   // every readable task, oldest first by created_at, then by id
	reclaimed []string // the running tasks reclaim failed, their workers gone
	Notes
}

// load reads every task in dir. An unreadable task file is skipped and left
// as it is (P10); a directory that cannot be listed is an error.
// (Readdirnames rather than os.ReadDir keeps the binary smaller, P14.)
func load(dir string) (snapshot, error) {
	f, err := os.Open(dir)
	if err != nil {
		return snapshot{}, err
	}
	names, err := f.Readdirnames(-1)
	f.Close()
	if err != nil {
		return snapshot{}, err
	}
	return collect(dir, names), nil
}

// collect reads the tasks that names, a listing of dir, holds, and also a
// task whose lock file is listed without its record: a listing can miss a
// record a worker renames over without the directory lock (modify), and such
// a task was claimed with a lock file, never renamed or removed (P6).
func collect(dir string, names []string) snapshot {
	var s snapshot
	ids := make(map[string]bool, len(names)) // id: whether its record is listed
	for _, name := range names {
		if strings.HasPrefix(name, ".") {
			continue
		}
		if id, ok := strings.CutSuffix(name, ".json"); ok {
			ids[id] = true
		} else if id, ok := strings.CutSuffix(name, ".lock"); ok && !ids[id] {
			ids[id] = false
		}
	}
	for id, listed := range ids {
		t, err := read(dir, id)
		if err == nil {
			s.tasks = append(s.tasks, t)
		} else if listed || !errors.Is(err, os.ErrNotExist) { // not a lock file whose record is gone
			s.Skipped = append(s.Skipped, id)
		}
	}
	slices.SortFunc(s.tasks, func(a, b Task) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	slices.Sort(s.Skipped)
	return s
}

// request is the shell request that runs t in environ.
func (t Task) request(environ []string) shell.Request {
	return shell.Request{
		Agent: t.Agent, Prompt: t.Prompt, Dir: t.Cwd, Timeout: time.Duration(t.TimeoutMS) * time.Millisecond,
		Environ: environ, Pass: t.Env, Session: t.Session, Model: t.Model, Write: t.Write, AllowTools: t.AllowTools,
	}
}

// labelRule says what ValidLabel accepts, for messages.
const labelRule = "must be letters, digits, '.', '_' or '-', start with a letter or digit, and be at most 100 bytes"

// ValidLabel reports whether label can label a task (#131).
func ValidLabel(label string) bool {
	alnum := func(c rune) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' }
	if label == "" || len(label) > 100 || !alnum(rune(label[0])) {
		return false
	}
	return !strings.ContainsFunc(label, func(c rune) bool { return !alnum(c) && !strings.ContainsRune("._-", c) })
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
	f, fi, err := store.OpenRegular(path(dir, id), os.O_RDONLY, 0)
	if err != nil {
		return Task{}, err
	}
	defer f.Close()
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
	case !slices.Contains(Statuses(), t.Status):
		return Task{}, fmt.Errorf("unknown status %q", t.Status)
	case t.Status == Running && t.Lease == nil:
		return Task{}, errors.New("running without a lease")
	}
	if t.Lease != nil {
		t.Lease.ActiveAt = time.Time{}
		if fi, err := os.Lstat(beatPath(dir, id)); err == nil {
			t.Lease.ActiveAt = fi.ModTime().UTC()
		}
	}
	return t, nil
}

func write(dir string, t Task) error {
	if t.Lease != nil && !t.Lease.ActiveAt.IsZero() { // read from the beat file, not stored
		l := *t.Lease
		l.ActiveAt = time.Time{}
		t.Lease = &l
	}
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return store.WriteFile(path(dir, t.ID), append(b, '\n'))
}
