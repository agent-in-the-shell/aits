# Task follow-ups (`task create --after`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `aits task create --after ID PROMPT` queues a follow-up that a worker runs in the parent task's agent session, once the parent is `done`.

**Architecture:** A task gains an optional `after` field. `Create` reads the parent under the directory lock, inherits its agent and cwd, and refuses a second live follow-up. `claim` passes over a follow-up until its parent is `done`, then returns the parent's session id for the run. `reclaim` fails queued follow-ups whose parent failed, is gone, or is done without a session, until nothing changes.

**Tech Stack:** Go 1.26, standard library only; tests run the built binary with the fake vendor (`internal/shell/testdata/fakevendor`).

**Spec:** `docs/superpowers/specs/2026-10-15-task-follow-up-design.md` (approved).

## Global Constraints

- No new error codes; no new run status: a follow-up that cannot run is `failed` with run status `not_started` (spec §2).
- No new dependency (P3, P14). The linux/amd64 release build stays under 3,145,728 bytes; it is 2,793,634 on main.
- Do not use Go 1.26 `new(expr)`: the CI `staticcheck` flags it.
- Text output of `list`, `show` and `work` is unchanged; `list --json` items and the task record gain `after` only when set (`omitempty`).
- Help is the contract: every rule a caller can observe is in `--help`, and `docs/cli.md` is regenerated with `go test ./cmd/aits -run TestCLIDocs -update` (P5, P13).
- Repo docs, commits and PR text in English. Commits end with the session's attribution lines; use `$SP/commit.sh` with `REF='#63'`.
- Never `pkill`/`kill` anything except processes started from the test's own temp directories.

## Review Focus

1. A follow-up whose parent is `running` in another worker must not be claimed, even though it is the oldest queued task in its agent's queue — a claim that ignores `after` resumes a session mid-turn (Task 2 unit test; Task 3 binary test 2).
2. `--after "$id"` with an empty `$id` must be `invalid_input`, never a new-conversation task (Task 3).
3. A chain whose root fails must fail entirely in one `list`, whatever the `created_at` order of its members (Task 2 unit test with a follow-up whose `created_at` is earlier than its parent's).
4. A `done` parent whose record has no `run` must not crash `claim` or `reclaim` (nil pointer) — the follow-up fails with `has no session` (Task 2 unit test).
5. An unreadable task file in the directory makes `create --after` fail closed, while plain `create` is unaffected (Task 1 unit test).

---

### Task 1: The `after` field and `Create`

**Files:**
- Modify: `internal/task/task.go` (`Task`, `Spec`, `Create`; new `validate`, `follow`)
- Test: `internal/task/task_test.go`

**Interfaces:**
- Produces: `Task.After string` (`json:"after,omitempty"`), `Spec.After string`; `Create(environ []string, s Spec) (Task, error)` unchanged in signature. Errors: `*shell.InvalidRequestError` for every refusal in spec §4's first table except an unreadable file, which is a plain error (CLI maps it to `internal`).

- [ ] **Step 1: Write the failing unit tests**

Add to `internal/task/task_test.go`:

```go
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

// A follow-up takes its parent's agent and cwd; differing ones, a failed or
// unknown parent and a second live follow-up are refused (spec §2, §4).
func TestCreateFollowUp(t *testing.T) {
	environ := env(t)
	p := create(t, environ, "codex")
	f, err := follow(environ, p.ID, Spec{})
	if err != nil || f.After != p.ID || f.Agent != "codex" || f.Cwd != p.Cwd {
		t.Fatalf("follow-up = %+v, %v; want agent and cwd of %+v", f, err, p)
	}
	if _, err := follow(environ, p.ID, Spec{Agent: "codex", Cwd: p.Cwd}); err == nil ||
		err.Error() != "invalid request: task "+p.ID+" already has a follow-up: "+f.ID+"; follow up on "+f.ID+" instead" {
		t.Errorf("second follow-up: err = %v", err)
	}
	g := create(t, environ, "claude")
	other := t.TempDir()
	for name, s := range map[string]Spec{
		"other agent": {Agent: "codex"},
		"other cwd":   {Cwd: other},
	} {
		var invalid *shell.InvalidRequestError
		if _, err := follow(environ, g.ID, s); !errors.As(err, &invalid) {
			t.Errorf("%s: err = %v, want invalid", name, err)
		}
	}
	for name, parent := range map[string]string{"unknown": "20000101T000000-000000", "not an id": "x"} {
		var invalid *shell.InvalidRequestError
		if _, err := follow(environ, parent, Spec{}); !errors.As(err, &invalid) || invalid.Reason != "no task "+parent {
			t.Errorf("%s parent: err = %v", name, err)
		}
	}
	setStatus(t, environ, g, Failed, &Run{Result: shell.Result{Status: "timeout", SessionID: "s"}})
	if _, err := follow(environ, g.ID, Spec{}); err == nil || err.Error() != "invalid request: task "+g.ID+" failed; only a done task can be followed up" {
		t.Errorf("failed parent: err = %v", err)
	}
}

// A failed follow-up never runs again, so it does not count: the parent can
// be followed up again (spec §2, fan-out).
func TestCreateFollowUpAfterFailedOne(t *testing.T) {
	environ := env(t)
	p := create(t, environ, "claude")
	f, err := follow(environ, p.ID, Spec{})
	if err != nil {
		t.Fatal(err)
	}
	setStatus(t, environ, f, Failed, &Run{Result: shell.Result{Status: NotStarted}})
	if _, err := follow(environ, p.ID, Spec{}); err != nil {
		t.Errorf("follow-up after a failed one: %v", err)
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 ./internal/task -run 'TestCreateFollowUp'`
Expected: compile failure, `unknown field After in struct literal of type Spec`.

- [ ] **Step 3: Implement**

In `internal/task/task.go`:

```go
type Task struct {
	ID        string    `json:"id"`
	Status    Status    `json:"status"`
	Agent     string    `json:"agent"`
	Cwd       string    `json:"cwd"`
	Prompt    string    `json:"prompt"`
	TimeoutMS int64     `json:"timeout_ms"`
	Env       []string  `json:"env,omitempty"` // names only (P8)
	After     string    `json:"after,omitempty"` // the task whose session this one continues (#63)
	CreatedAt time.Time `json:"created_at"`
	Lease     *Lease    `json:"lease"`
	Run       *Run      `json:"run"`
}

// Spec is a task to create. With After set, an empty Agent or Cwd is taken
// from that task.
type Spec struct {
	Agent, Cwd, Prompt, After string
	Timeout                   time.Duration
	Env                       []string
}
```

Replace the validation at the top of `Create` and add the follow-up step after the lock:

```go
// Create validates s as the request it will be run as, and queues it. A
// follow-up (s.After) is checked against its parent under the lock.
func Create(environ []string, s Spec) (Task, error) {
	now := time.Now().UTC()
	t := Task{
		Status: Queued, Agent: s.Agent, Cwd: s.Cwd, Prompt: s.Prompt,
		TimeoutMS: s.Timeout.Milliseconds(), Env: s.Env, After: s.After, CreatedAt: now,
	}
	if s.After == "" {
		if err := t.validate(s.Timeout); err != nil {
			return Task{}, err
		}
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
	if s.After != "" {
		if err := t.follow(dir, s.Timeout); err != nil {
			return Task{}, err
		}
	}
	for {
		// … unchanged id loop …
	}
	return t, write(dir, t)
}

// validate checks t as the request it will be run as, with timeout as given
// rather than its stored form (P6).
func (t Task) validate(timeout time.Duration) error {
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
// parent must exist and not have failed, t takes its agent and cwd, and the
// parent may have no other follow-up that is not failed (#63).
func (t *Task) follow(dir string, timeout time.Duration) error {
	p, err := read(dir, t.After)
	if errors.Is(err, os.ErrNotExist) {
		return invalid("no task %s", t.After)
	}
	if err != nil {
		return err
	}
	switch {
	case p.Status == Failed:
		return invalid("task %s failed; only a done task can be followed up", p.ID)
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
		if o.After == p.ID && o.Status != Failed {
			return invalid("task %s already has a follow-up: %s; follow up on %s instead", p.ID, o.ID, o.ID)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run the package tests**

Run: `go test -count=1 ./internal/task`
Expected: PASS, including `TestCreateValidates` (a plain create still validates before the lock).

- [ ] **Step 5: Commit**

```bash
git add internal/task/task.go internal/task/task_test.go
REF='#63' $SP/commit.sh "feat(task): a task can be created as the follow-up of another"
```

---

### Task 2: Claim waits for the parent; reclaim fails orphans

**Files:**
- Modify: `internal/task/task.go` (`Work`, `claim`, `reclaim`; new `parentFault`)
- Test: `internal/task/task_test.go`

**Interfaces:**
- Consumes: `Task.After`, `follow`/`setStatus` test helpers from Task 1.
- Produces: `claim(ctx, dir, agent string) (*Task, string, func(), Notes, error)` — the second value is the session the claimed task continues ("" for a task without `after`). `Work` sets `Request.Session` from it.

- [ ] **Step 1: Write the failing unit tests**

```go
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
```

Update the existing callers of `claim` in `internal/task/task_test.go` (`TestClaimLockHeld` and any other) to the five-value form (`got, _, release, notes, err := claim(...)`).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 ./internal/task -run 'TestClaimFollowUp|TestReclaimFailsOrphans|TestReclaimSparesFollowUp'`
Expected: compile failure on the five-value `claim` (`assignment mismatch`).

- [ ] **Step 3: Implement**

In `reclaim`, after the loop over running tasks and before `slices.Sort(s.Unknown)`:

```go
	// Fail queued follow-ups that can never run, until a pass changes
	// nothing, so a whole chain fails at once whatever its created_at order.
	byID := make(map[string]*Task, len(s.tasks))
	for i := range s.tasks {
		byID[s.tasks[i].ID] = &s.tasks[i]
	}
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
				return s, groups, err
			}
			changed = true
		}
	}
```

and the helper:

```go
// parentFault says why a follow-up of p can never run, or "" if it may yet:
// p failed, its file is gone, or it is done without a session. A parent whose
// file cannot be read (skipped) is waited for (P10).
func parentFault(p *Task, skipped bool) string {
	switch {
	case p == nil && !skipped:
		return "does not exist"
	case p == nil:
		return ""
	case p.Status == Failed:
		return "failed"
	case p.Status == Done && (p.Run == nil || p.Run.SessionID == ""):
		return "has no session"
	}
	return ""
}
```

In `claim`, build the same index after `reclaim` and pick a claimable task:

```go
	s, groups, err := reclaim(dir)
	byID := make(map[string]*Task, len(s.tasks))
	for i := range s.tasks {
		byID[s.tasks[i].ID] = &s.tasks[i]
	}
	var claimed *Task
	var session string
	var release func()
	i := slices.IndexFunc(s.tasks, func(t Task) bool {
		if t.Status != Queued || (agent != "" && t.Agent != agent) {
			return false
		}
		p := byID[t.After]
		return t.After == "" || (p != nil && p.Status == Done)
	})
	if err == nil && i >= 0 {
		t := &s.tasks[i]
		if t.After != "" {
			session = byID[t.After].Run.SessionID // reclaim failed any done parent without one
		}
		// … unchanged TryLock / write …
	}
	unlock()
	shell.Stop(groups...)
	return claimed, session, release, s.Notes, err
```

(If `reclaim` and `claim` both build the index, move it into one `index(tasks []Task) map[string]*Task` helper.)

In `Work`:

```go
	t, session, release, notes, err := claim(ctx, dir, agent)
	…
	req := t.request(environ)
	req.Session = session
	req.Kill = kill
```

Update the `claim` doc comment: it returns the session the task continues.

- [ ] **Step 4: Run the package tests**

Run: `go test -count=1 ./internal/task`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/task/task.go internal/task/task_test.go
REF='#63' $SP/commit.sh "feat(task): run a follow-up in its parent's session once the parent is done"
```

---

### Task 3: CLI, help and binary tests

**Files:**
- Modify: `internal/cli/task.go` (`taskFields`, `taskCreate`, `taskList`, `taskWork`)
- Modify: `docs/cli.md` (regenerated)
- Test: `cmd/aits/task_test.go` (`taskRecord`, `taskList`, new tests)

**Interfaces:**
- Consumes: `task.Spec.After`, `Task.After` (Task 1); follow-up claim and reclaim (Task 2).
- Produces: the `--after` flag; `after` in `list --json` items.

- [ ] **Step 1: Write the failing binary tests**

Add `After string `json:"after"`` to `taskRecord` (after `Env`) and to the `taskList` item struct. Then add to `cmd/aits/task_test.go`:

```go
// after runs task create --after parent with flags and returns the result.
func (q queue) after(parent, prompt string, flags ...string) output {
	q.t.Helper()
	return q.run(append(append([]string{"create", "--after", parent, "--json"}, flags...), prompt)...)
}

// A follow-up runs in its parent's session, with its agent and cwd, given
// neither (#63). The parent's session id is patched to a unique value: the
// fake always reports sess-fake-1.
func TestTaskFollowUpResumesParentSession(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		q := queue{t: t, home: t.TempDir(), path: vendorAs(t, agent, "ok")}
		p := q.create("review", "--agent", agent)
		if out := q.run("work"); out.code != 0 {
			t.Fatalf("work: code=%d stderr=%q", out.code, out.stderr)
		}
		parent := decodeOne[taskRecord](t, q.run("show", p, "--json").stdout)
		q.patch(p, map[string]any{"run": map[string]any{"status": "ok", "agent": agent, "session_id": "sess-parent-7", "answer": "a", "finished_at": parent.Run.FinishedAt}})
		f := decodeCreated(t, q.after(p, "fix it"))
		w := decodeOne[taskRecord](t, q.run("work", "--json").stdout)
		if w.ID != f || w.Status != "done" || w.After != p || w.Agent != agent || w.Cwd != parent.Cwd {
			t.Errorf("follow-up = %+v", w)
		}
		rec := readRecord(t, q.path)
		want := map[string][]string{"claude": {"--resume", "sess-parent-7"}, "codex": {"resume", "sess-parent-7"}}[agent]
		if i := slices.Index(rec.Args, want[0]); i < 0 || i+1 >= len(rec.Args) || rec.Args[i+1] != want[1] {
			t.Errorf("agent argv = %q, want %q", rec.Args, want)
		}
	})
}

// While a worker runs the parent, a second worker passes over its follow-up
// and claims a newer task; the follow-up stays queued. When the parent then
// fails, list fails the follow-up (#63).
func TestTaskFollowUpWaitsForParent(t *testing.T) {
	q := newQueue(t, "sleep")
	p := q.create("parent")
	f := decodeCreated(t, q.after(p, "follow"))
	x := q.create("other")
	s := call{path: q.path, env: []string{"AITS_HOME=" + q.home}, args: []string{"task", "work", "--json"}}.start(t)
	waitFile(t, filepath.Join(q.path, "child.pid"))
	q.waitLease(p, readPID(t, filepath.Join(q.path, "child.pid")))
	other := call{path: vendor(t, "ok"), env: []string{"AITS_HOME=" + q.home}, args: []string{"task", "work", "--json"}}.run(t)
	if w := decodeOne[taskRecord](t, other.stdout); w.ID != x {
		t.Errorf("second worker claimed %s, want %s (not the follow-up %s)", w.ID, x, f)
	}
	if r := decodeOne[taskRecord](t, q.run("show", f, "--json").stdout); r.Status != "queued" {
		t.Errorf("follow-up = %+v, want queued", r)
	}
	s.signal(t, syscall.SIGTERM)
	s.waitOrKill(t, 10*time.Second)
	q.run("list")
	if r := decodeOne[taskRecord](t, q.run("show", f, "--json").stdout); r.Status != "failed" || r.Run == nil ||
		r.Run.Status != "not_started" || r.Run.Message != "its parent task "+p+" failed" {
		t.Errorf("follow-up of a failed parent = %+v", r)
	}
}

// create --after refuses what it cannot run as asked, and writes nothing
// (#63, spec §4).
func TestTaskCreateAfterInvalid(t *testing.T) {
	q := newQueue(t, "ok")
	p := q.create("parent")
	parent := decodeOne[taskRecord](t, q.run("show", p, "--json").stdout)
	first := decodeCreated(t, q.after(p, "first"))
	failed := q.create("failed")
	q.patch(failed, map[string]any{"status": "failed", "run": map[string]any{"status": "timeout", "agent": "claude", "finished_at": parent.CreatedAt}})
	for name, args := range map[string][]string{
		"unknown parent":  {"create", "--after", "20000101T000000-000000", "x"},
		"empty after":     {"create", "--after", "", "x"},
		"failed parent":   {"create", "--after", failed, "x"},
		"other agent":     {"create", "--after", p, "--agent", "codex", "x"},
		"other cwd":       {"create", "--after", p, "--cwd", t.TempDir(), "x"},
		"second follower": {"create", "--after", p, "x"},
	} {
		before := q.count()
		out := q.run(append(args, "--json")...)
		if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "invalid_input" || q.count() != before {
			t.Errorf("%s: code=%d stdout=%q", name, out.code, out.stdout)
		}
	}
	// The same cwd, spelled with a trailing slash, is the parent's.
	q.patch(first, map[string]any{"status": "failed", "run": map[string]any{"status": "not_started", "agent": "claude", "finished_at": parent.CreatedAt}})
	if out := q.after(p, "again", "--cwd", parent.Cwd+"/", "--agent", "claude"); out.code != 0 {
		t.Errorf("same agent and cwd, first follow-up failed: code=%d stdout=%q", out.code, out.stdout)
	}
}
```

with two small helpers next to `queuedAny`:

```go
// count returns how many task records exist.
func (q queue) count() int {
	entries, _ := os.ReadDir(filepath.Join(q.home, "task"))
	return len(slices.DeleteFunc(entries, func(e os.DirEntry) bool { return !strings.HasSuffix(e.Name(), ".json") }))
}

// decodeCreated returns the id a successful create --json printed.
func decodeCreated(t *testing.T, out output) string {
	t.Helper()
	var r struct{ ID, Status string }
	if err := json.Unmarshal([]byte(out.stdout), &r); err != nil || r.ID == "" {
		t.Fatalf("create: code=%d stdout=%q", out.code, out.stdout)
	}
	return r.ID
}
```


Also add a `TestOutputChannels` row in `cmd/aits/output_test.go`:

```go
		{name: "task create after unknown", scenario: "ok", args: []string{"task", "create", "--after", "20000101T000000-000000", "hi"}, code: 2,
			errCode: "invalid_input", errMsg: "no task 20000101T000000-000000"},
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 ./cmd/aits -run 'TestTaskFollowUp|TestTaskCreateAfterInvalid|TestOutputChannels'`
Expected: FAIL — `unknown flag --after` for every follow-up case.

- [ ] **Step 3: Implement the CLI and help**

In `taskCreate`:

```go
			agent := fs.String("agent", "", "`AGENT` CLI to run (required unless --after). One of: "+strings.Join(shell.Agents(), ", ")+".")
			cwd := nonEmpty(fs, "cwd", "Run the agent in `DIR` (default: the current directory, or with --after the parent's).", "a directory")
			after := nonEmpty(fs, "after", "Continue the conversation of task `ID` once it is done, with its agent and cwd.", "a task id")
			…
			return func(ctx context.Context, env *Env, args []string) (int, error) {
				prompt, dir, err := promptArgs(ctx, env, args, *cwd)
				if err != nil {
					return 0, err
				}
				if *after != "" && *cwd == "" {
					dir = "" // the parent's
				}
				t, err := task.Create(env.Environ, task.Spec{Agent: *agent, Cwd: dir, Prompt: prompt, After: *after, Timeout: *timeout, Env: pass})
```

Add to `taskCreate`'s `Details`, after the output line:

```
Follow-ups (--after ID): the task continues task ID's conversation, with its
agent and cwd; --agent or --cwd, if given, must be the same (--cwd is made
absolute, symlinks not resolved). It is claimed only once ID is done, and
runs with ID's session id. If ID fails, the next list or work fails the
follow-up, with run status not_started; until then show reports it queued.
ID must not have failed, and may have one follow-up that has not failed.
If a task file cannot be read, whether ID already has one cannot be told,
and create fails (internal).
```

In `taskFields`, add a line after `env`:

```
  after        For a follow-up: the task whose session it continues.
```

and change the `not_started` wording to "not_started (the agent was never started: it could not be started after the claim, or the task's parent failed)".

In `taskList`: the `--json` shape line becomes `{"tasks":[{"id","status","agent","cwd","created_at","after"}],"skipped":["ID"]}` with "after only for a follow-up", the `item` struct gains `After string `json:"after,omitempty"``, and the reclaim sentence becomes "Tasks of dead workers are reclaimed first, and follow-ups of failed tasks are failed."

In `taskWork`: `Summary: "Claim the oldest claimable task, run it, record the result, exit."` and the first sentence of `Details`: "With nothing claimable (nothing queued, or only follow-ups whose parent is not done), work prints {"status":"empty"} …".

- [ ] **Step 4: Regenerate docs and run the tests**

Run: `go test -count=1 ./cmd/aits -run TestCLIDocs -update && go test -count=1 ./...`
Expected: PASS. (Remove leftover `.claude/worktrees/wf_*` first if `TestOnlyStoreWrites` fails.)

- [ ] **Step 5: Commit**

```bash
git add internal/cli/task.go docs/cli.md cmd/aits/task_test.go cmd/aits/output_test.go
REF='#63' $SP/commit.sh "feat(cli): task create --after queues a follow-up in the same session"
```

---

### Task 4: Docs, size and the issue

**Files:**
- Modify: `docs/superpowers/specs/2026-10-14-task-queue-design.md` (§1, §4, §6)
- Modify: `README.md` (task row and example)

- [ ] **Step 1: Point the task-queue spec at the follow-up spec**

§1: replace "Follow-ups on a task's session come in a later issue." with "Follow-ups on a task's session: `2026-10-15-task-follow-up-design.md` (#63)." §4: after the `run` bullet add "- `after`: for a follow-up, the task whose session it continues; `not_started` also covers a follow-up whose parent failed (#63)." §6 **Claim**: "pick the oldest claimable `queued` task — a follow-up only once its parent is `done` (#63) — …".

- [ ] **Step 2: README**

The `aits task` row reads "… run them through `shell`, and follow up on the same session (available)"; the example gains `next=$(aits task create --after "$id" "Fix the first bug you found")`.

- [ ] **Step 3: Measure and verify**

Run: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o $SP/aits63 ./cmd/aits && stat -f %z $SP/aits63`
Expected: below 3,145,728; record the figure and the change from 2,793,634 in the PR.
Run: `gofmt -l . ; go vet ./... && staticcheck -tags live ./... && go test -count=1 ./...`
Expected: no gofmt output; PASS.

- [ ] **Step 4: Commit and note the fan-out change on the issue**

```bash
git add docs/superpowers/specs/2026-10-14-task-queue-design.md README.md
REF='#63' $SP/commit.sh "docs: task follow-ups are available"
gh issue comment 63 --body-file $SP/issue63-fanout.md
```

`$SP/issue63-fanout.md` says: the approved spec refuses a second follow-up of a task while its first has not failed, narrowing this issue's out-of-scope note, because two follow-ups could resume one session concurrently; a failed follow-up does not count, so it can be retried.
