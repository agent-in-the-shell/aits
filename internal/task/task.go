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

// Run statuses besides shell's: the agent was never started after the claim,
// or the worker exited before it recorded a result.
const (
	NotStarted shell.Status = "not_started"
	WorkerDied shell.Status = "worker_died"
)

// Task is one queued agent run and, once worked, its result.
type Task struct {
	ID        string    `json:"id"`
	Status    Status    `json:"status"`
	Agent     string    `json:"agent"`
	Cwd       string    `json:"cwd"`
	Prompt    string    `json:"prompt"`
	TimeoutMS int64     `json:"timeout_ms"`
	Env       []string  `json:"env,omitempty"`   // names only (P8)
	After     string    `json:"after,omitempty"` // the task whose session this one continues (#63)
	Write     bool      `json:"write,omitempty"` // set by create --write only, for this task alone (P9, #104)
	CreatedAt time.Time `json:"created_at"`
	Lease     *Lease    `json:"lease"`
	Run       *Run      `json:"run"`
}

// Lease is a worker's claim on a running task. Whether the worker is alive is
// not recorded: it holds the task's lock file while it runs (lockPath).
type Lease struct {
	Token     string    `json:"token"`
	PGID      int       `json:"pgid"`                // the agent's process group; 0 until it starts
	StartedAt time.Time `json:"started_at,omitzero"` // when the agent started, recorded with PGID
}

// Run is the outcome of working a task.
type Run struct {
	shell.Result
	StartedAt  time.Time `json:"started_at,omitzero"` // absent when the agent never ran
	FinishedAt time.Time `json:"finished_at"`
}

// Spec is a task to create. With After set, an empty Agent or Cwd is taken
// from that task.
type Spec struct {
	Agent, Cwd, Prompt, After string
	Timeout                   time.Duration
	Env                       []string
	Write                     bool // never taken from After (#104)
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

// Create validates s as the request it will be run as, and queues it. A
// follow-up (s.After) is checked against its parent under the lock.
func Create(environ []string, s Spec) (Task, error) {
	now := time.Now().UTC()
	t := Task{
		Status: Queued, Agent: s.Agent, Cwd: s.Cwd, Prompt: s.Prompt,
		TimeoutMS: s.Timeout.Milliseconds(), Env: s.Env, After: s.After, Write: s.Write, CreatedAt: now,
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
	return t, write(dir, t)
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
		if o.Run == nil || o.Run.Status != WorkerDied {
			continue // its agent had ended, or never ran, before it was recorded failed
		}
		// Its agent may still be running until the reclaim that failed it has
		// stopped it; that reclaim holds its lock until then.
		release, err := store.TryLock(lockPath(dir, o.ID), false)
		switch {
		case errors.Is(err, store.ErrLocked):
			return invalid("task %s, a follow-up of %s, failed but its agent is still being stopped; try again in a few seconds", o.ID, p.ID)
		case err == nil:
			release()
		case !errors.Is(err, os.ErrNotExist): // (no lock file: removed by hand, nothing to wait for)
			return fmt.Errorf("task %s: %w", o.ID, err)
		}
	}
	return nil
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

// Show returns one task (see find).
func Show(environ []string, id string) (Task, error) {
	dir, err := taskDir(environ)
	if err != nil {
		return Task{}, err
	}
	return find(dir, id)
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
// returns the notes of the reclaim. The task is nil when nothing is claimable;
// the error is shell.ErrStopped when ctx is done before the claim, and
// errReclaimed, wrapped with the task id, when the task was reclaimed.
func Work(ctx context.Context, kill <-chan struct{}, environ []string, agent string) (*Task, Notes, error) {
	if agent != "" && !slices.Contains(shell.Agents(), agent) {
		return nil, Notes{}, invalid("unknown agent %q (one of: %s)", agent, strings.Join(shell.Agents(), ", "))
	}
	dir, err := taskDir(environ)
	if err != nil {
		return nil, Notes{}, err
	}
	t, session, release, notes, err := claim(ctx, dir, agent)
	if t == nil || err != nil {
		return nil, notes, err
	}
	defer release() // the worker holds the task's lock until its run is recorded
	token := t.Lease.Token
	var started time.Time // stays zero unless the agent starts
	req := t.request(environ)
	req.Session = session
	req.Kill = kill
	req.Started = func(pgid int) error {
		started = time.Now().UTC()
		// Not update: waiting for the directory lock would leave a window, as
		// long as another command holds it, in which a killed worker's agent
		// is unrecorded and never stopped (P10). As holder of the task's lock
		// the worker is the only writer of its running record.
		if _, err := modify(dir, t.ID, token, func(t *Task) { t.Lease.PGID, t.Lease.StartedAt = pgid, started }); err != nil {
			return fmt.Errorf("the agent's process group could not be recorded: %w", err)
		}
		return nil
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
		return nil, notes, err
	}
	return &done, notes, nil
}

// claim, under the lock, reclaims tasks of dead workers and marks the oldest
// claimable queued task of agent running with a fresh lease. It locks the
// task's lock file first, so that a running task's lock is never free while
// its worker lives, and returns the session the task continues ("" for a new
// one), the function that releases the lock, and the notes of the reclaim. A
// signal that arrived while it waited for the lock claims nothing.
func claim(ctx context.Context, dir, agent string) (*Task, string, func(), Notes, error) {
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
		return t.Status == Queued && (agent == "" || t.Agent == agent) && claimable(t, byID)
	})
	if err == nil && i >= 0 {
		t := &s.tasks[i]
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
	tasks []Task // every readable task, oldest first by created_at, then by id
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
		Environ: environ, Pass: t.Env, Write: t.Write,
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
