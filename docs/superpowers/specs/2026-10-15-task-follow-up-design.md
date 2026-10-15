# aits task follow-ups — design

Issue: #63. Status: approved.
Revises: `2026-10-14-task-queue-design.md` §1, §4, §5, §6.
Principles: P2, P4, P5, P6, P10, P11, P12, P13, P14.

## 1. Goal

A dispatcher can queue a follow-up prompt that continues the conversation of
an earlier task, in one command, before that task has finished:

```
id=$(aits task create --agent claude --cwd ~/src/proj "Review internal/session for bugs")
next=$(aits task create --after "$id" "Fix the first bug you found")
aits task work   # runs $id
aits task work   # runs $next in $id's session, with the same agent and cwd
```

Today the dispatcher must wait for the task, read `run.session_id` and leave
the queue for `shell submit --session`. That is the two-step follow-up P10
names as a failure to avoid. The task-queue spec deferred this (§1).

## 2. Decisions

| Topic | Decision | Basis |
|---|---|---|
| Naming the parent | `task create --after ID`; the record keeps `"after":"ID"`. `--after` refuses an empty value, like `--cwd` | P10: one command; P4: the flag and the field share a name; P6: `--after "$id"` with an empty `$id` must not queue a task that starts a new conversation |
| Agent and cwd | Taken from the parent. `--agent` or `--cwd` given with a different value is `invalid_input`; the same value is accepted. `--cwd` is compared after it is made absolute, without resolving symlinks | A session belongs to one agent and one directory; P6: refuse rather than silently override |
| Prompt, timeout, `--env` | The follow-up's own, as for any task; not inherited | P2: no caller needs inheritance |
| Session | Not stored at create. The worker that claims the follow-up runs it with the parent's `run.session_id` | The follow-up can be queued while its parent is queued or running |
| When it can be claimed | Only once its parent is `done`. `work` passes over it until then. Otherwise the oldest claimable task is taken, as today | P10: no dispatcher polling; P6: a session is continued only after a turn that finished |
| A parent that fails | The follow-up becomes `failed` the next time `list` or `work` reclaims, with run status `not_started` and the message `its parent task ID failed`. This also covers a parent that cannot have been followed: its file is gone (`does not exist`), or it is `done` with no session id (`has no session`) | P6; no new status (P14): `not_started` already means the agent never ran |
| Parent already `failed`, or `done` without a session id, at create | `invalid_input`: `task ID failed; only a done task with a session can be followed up` (or `has no session; …`) | P6: refuse at once rather than queue a task that can only fail |
| Chains | A follow-up of a follow-up works the same way. `--after` names a task that exists, so no cycle can be built | — |
| Fan-out | A task has at most one follow-up that is not `failed`. A second `--after` on the same parent is `invalid_input`, naming the existing one. A failed follow-up does not count, except a `worker_died` one until its reclaim has stopped its agent (#76), so it can be retried with another `--after` on the same parent | Codex resumes a session in place (shell-codex spec §4). Two follow-ups of one parent could run at once in two workers and write one conversation concurrently; that is undefined, so it is refused (P6). **This narrows #63**, whose out-of-scope note said fan-out "needs no extra work" (P11: the deviation is stated here) |
| An unreadable task file at create | `--after` fails with `internal`, naming the file, and writes nothing: whether it is a follow-up of the same parent cannot be told | P6: cannot determine is not allowed through. `list` and `work` are unaffected |
| A parent that cannot be read at work | The follow-up stays `queued` and is passed over; the parent is reported as skipped, as today | P10: an unreadable task file is left as it is |
| New error codes | None | P14 |

## 3. Changes

**`internal/task`**
- `Task.After string` (`json:"after,omitempty"`); `Spec.After string`.
- `Create` with `After` set does everything under the directory lock, in
  this order:
  1. read the parent: an unknown id is `invalid_input` (`no task ID`); an
     unreadable one is the read error (`internal`);
  2. a `failed` parent, or a `done` one without a session id, is
     `invalid_input`;
  3. the spec's agent and cwd must be empty or equal to the parent's; the
     task takes the parent's;
  4. validate the whole request as today (prompt, timeout, `--env` names,
     the cwd still a directory);
  5. load every task: any skipped file is `internal`; a task whose `after`
     is the parent and whose status is not `failed` is `invalid_input`
     (`task ID already has a follow-up: ID2; follow up on ID2 instead`).

  Without `After`, `Create` is unchanged: it validates before taking the
  lock.
- `reclaim`, after failing the tasks of dead workers, fails each `queued`
  follow-up whose parent is `failed`, gone, or `done` without a session id.
  It repeats until a pass changes nothing, so a whole chain fails in one
  call. It does not rely on `created_at` order, which follows the wall clock.
- `load` also reads a task whose lock file is listed without its record: a
  listing can miss a record a worker renames over without the directory lock
  (its pgid write), and such a task was claimed with a lock file, which is
  never renamed or removed. So neither a parent (taken for gone) nor a live
  follow-up (missed by the fan-out check) is overlooked (P6).
- `claim` takes the oldest `queued` task matching `--agent` whose `after` is
  empty or names a `done` task in the snapshot, and runs it with
  `Request.Session` set to that task's `run.session_id`.

**`internal/cli/task.go`**
- `task create` gains `--after ID`. With `--after`, only an explicit `--cwd`
  is passed on (it defaults to the current directory), made absolute; an
  explicit `--agent` likewise.
- Help (P5, P13):
  - `create`: `--after` and the rules of §2; `--agent` reads "required
    unless --after";
  - `work`: it claims "the oldest claimable task"; `{"status":"empty"}` and
    its exit-code row mean nothing is claimable, which includes follow-ups
    waiting on their parent;
  - the task fields: `after`; `not_started`, like `list` and `create`, names
    every reason a follow-up is failed (its parent does not exist, failed or
    has no session when done), and `list` says a follow-up of an unreadable
    task stays queued (#79);
  - `list --json` items gain `after` when set. Text output is unchanged.
- `show` does not reclaim, so a follow-up of a failed parent shows `queued`
  until the next `list` or `work`; the `create` help says so.

**Docs**: `docs/cli.md` regenerated; the task-queue spec §1, §4 and §6 point
here; the README marks follow-ups available; issue #63 notes the fan-out
change.

## 4. Behaviour by case

| Parent when the follow-up is created | Result |
|---|---|
| unknown id, or `--after ""` | `invalid_input`, nothing written |
| `queued` or `running` | queued; waits |
| `done` | queued; claimable at once |
| `failed` | `invalid_input`, nothing written |
| `done` without a session id | `invalid_input`, nothing written |
| has a follow-up that is not `failed` | `invalid_input`, naming it |
| has only `failed` follow-ups | queued |
| a task file in the directory cannot be read | `internal`, nothing written |

| Parent when `list` or `work` looks at the queued follow-up | Result |
|---|---|
| `queued` or `running` | passed over |
| `done` with a session id | claimable; runs with that session |
| `failed`, including `worker_died` found in the same reclaim | follow-up `failed`, `not_started`, `its parent task ID failed` |
| `done` without a session id | follow-up `failed`, `not_started`, `its parent task ID has no session` |
| file gone | follow-up `failed`, `not_started`, `its parent task ID does not exist` |
| file cannot be read | passed over; the parent is in `skipped` |

## 5. Known limits

- A worker of an earlier aits ignores `after`: it can claim a follow-up
  before its parent is done and run it in a new conversation, and its
  writes drop the field. As for #37, run one aits version per `AITS_HOME`.
- A worker that dies after its agent started but before it recorded the
  pgid, or a reclaim killed while it stops a dead worker's agent (liveness
  spec §5), leaves an agent nobody stops; a retry of that follow-up can run
  beside it.
- A lock file removed by hand (liveness spec §5) loses that fallback: if a
  listing then also misses its running task's record, a follow-up of it can
  be failed as `does not exist`, or a second follow-up created.
- Whether Claude Code resumes in place, as Codex does, is not observed in
  this repo; fan-out is refused either way, because concurrent resumes of
  one session are undefined.

## 6. Testing

Functional (`cmd/aits`, built binary, fake vendor):
1. **Right session.** A `done` parent whose `run.session_id` is a unique
   value (the record patched in the test; the fake always reports
   `sess-fake-1`). The follow-up's run has `--resume <that id>` (Claude) or
   `resume <that id>` (Codex) in the fake's recorded argv, with the
   parent's agent and cwd, and no `--agent` or `--cwd` given.
2. **Passed over while the parent runs.** Create P, then F `--after P`, then
   X. While a worker runs P (fake `sleep`), a second `work` claims X, not F;
   without the claim rule it would take F, the older one. (That F is claimed
   once its parent is `done` is test 1.)
3. **Failure reaches the chain.** P fails; one `list` fails F (after P) and G
   (after F), each `not_started` naming its parent. Also: parent file
   removed (`does not exist`); a `done` parent with no session id (`has no
   session`).
4. **Refused at create**, exit 2 and nothing written: unknown parent;
   `--after ""`; failed parent; a `done` parent without a session id; a
   different `--agent`; a different `--cwd`;
   a second follow-up of a parent whose first is queued. Exit 1, nothing
   written: an unreadable task file in the directory. Accepted: the same
   `--cwd` with a trailing slash; a second follow-up when the first failed.
5. **Output.** `show --json` and `list --json` carry `after`; the help of
   `create`, `work` and `list` says what §3 lists.

Unit (`internal/task`): the claim and reclaim rules of §4 as a table.

P14: the change is measured on the linux/amd64 release build and the
figure recorded in the pull request.

## 7. Out of scope

`--session` on `task create` (P2: no caller once `--after` exists); fan-out
(refused, §2); re-running a failed task.
