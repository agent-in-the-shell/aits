# `aits task` — Design

- Status: approved
- Issue: #32
- Principles: P1, P2, P3, P5, P6, P7, P8, P9, P10, P12, P13, P14 (see `docs/principles.md`)

## 1. Goal

A local queue of agent runs. A dispatching agent creates tasks; one or more
workers, each in the foreground, claim and run one task per call through
`shell`; the dispatcher reads results by id. First slice: `create`, `list`,
`show`, `work`, and the states `queued → running → done | failed`.
Follow-ups on a task's session come in a later issue.

## 2. Decisions

| Topic | Decision | Basis |
|---|---|---|
| Caller | An AI agent dispatching work to other agents | P2: the issue's use case |
| Storage | One JSON file per task under `$AITS_HOME/task/`, one `flock` lock for claims | P3, P14: about +40 KB measured; a SQLite driver costs +1.8 MB (cgo, breaks P3) to +4.5 MB (pure Go); no query needs today (P2) |
| Running | `task work` claims one task, runs it, records the run, exits; no loop, no daemon | P10 |
| Lease | Written at claim: random token, worker pid, expiry = task timeout + 1 minute; never renewed | P10; `shell` enforces the timeout itself, so a lease expires only when the worker died |
| Expired lease | Task set to `failed` (`lease_expired`), its leftover agent process group stopped; never re-queued | Re-running a task with side effects twice is worse than asking the dispatcher to resubmit |
| Stopping a leftover agent | `shell.Request` gains `Started func(pgid int)`; the worker records the pgid in the lease | Only way for `task` to know the agent's group; reclaim is its caller from day one (P2) |
| Permissions | None stored or forwarded; agents run with the vendors' safe defaults | P9 |
| Errors | No new error codes; an unknown id is `invalid_input` | P14: nothing new to document and test |

## 3. Packages

- `internal/task`: the command group — create, list, show, work, claim,
  reclaim, the state machine. Its only cross-group import is `internal/shell`
  (Go API, never exec'ing `aits`).
- `internal/store`: the storage package P7 names, three functions:
  - `Dir(environ []string, group string) (string, error)` — resolves
    `$AITS_HOME/<group>` (default `$HOME/.local/share/aits/<group>`), creates
    it 0700 and tightens an existing one to 0700;
  - `WriteFile(path string, data []byte) error` — temp file in the same
    directory, then rename; mode 0600;
  - `Lock(dir string) (unlock func(), err error)` — `flock` on `<dir>/.lock`.
- `internal/cli/task.go`: the `task` group, reusing `nonEmpty`, `readPrompt`
  and the error mapping of `shell submit`.
- `internal/shell`: `Request.Started func(pgid int)`, called once right after
  the agent starts; nil means not called.

## 4. Task record

`$AITS_HOME/task/<id>.json`, mode 0600. The id is the creation time plus a
random suffix from `math/rand/v2` (`20261014T112233-a3f9c2`), so name order is
creation order and `list` needs no index.

```json
{"id":"20261014T112233-a3f9c2","status":"done","agent":"claude","cwd":"/abs/dir",
 "prompt":"…","timeout_ms":1800000,"env":["GITHUB_TOKEN"],"created_at":"2026-10-14T11:22:33Z",
 "lease":null,
 "run":{"status":"ok","session_id":"…","answer":"…","exit_code":0,"duration_ms":12345,
        "stderr_tail":"","message":"","started_at":"…","finished_at":"…"}}
```

- `status`: `queued`, `running`, `done`, `failed`.
- `env`: variable names only; values are read from the worker's environment
  at `work` time (P8).
- `lease` (while `running`): `{"token","worker_pid","pgid","expires_at"}`;
  `pgid` is 0 until the agent has started.
- `run`: the fields of `shell.Result`, so answer and stderr are bounded as in
  `shell`; `run.status` is a shell status, or `not_started` when the agent
  could not be started after the claim (agent not on `PATH`, an `--env`
  variable unset in the worker's environment, start failure), with the
  reason in `message`.

## 5. Commands

All accept `--json`. Exit codes: 0 success, 1 operation failed, 2 usage or
invalid input (P5).

`aits task create --agent AGENT --cwd DIR [--timeout D] [--env NAME]… (PROMPT | -)`
- Validates as `shell submit` does (agent, `--cwd` an existing directory made
  absolute, timeout > 0, non-empty prompt, `--env` name syntax; explicitly
  empty values refused). Invalid input: exit 2, no file written.
- `--json`: `{"id":"…","status":"queued"}`; text: the id. Exit 0.

`aits task list`
- Reclaims expired leases (§6), then lists tasks oldest first.
- `--json`: `{"tasks":[{"id","status","agent","cwd","created_at"}]}`; text: one
  line per task, `ID  STATUS  AGENT  CREATED_AT`. Exit 0.

`aits task show ID`
- `--json`: the task record (§4); text: `ID  STATUS  AGENT  CWD`, then the
  session id and shell status when there is a run, then the answer.
- Unknown id: `invalid_input` (`no task ID`), exit 2.

`aits task work [--agent AGENT]`
- Reclaims, claims the oldest `queued` task (of `AGENT` if given), runs it,
  records the run, exits.

| Case | `--json` | text | exit |
|---|---|---|---|
| nothing queued | `{"status":"empty"}` | nothing | 0 |
| run status `ok` → task `done` | task record | answer on stdout; `task: ID`, `session: ID` on stderr | 0 |
| any other run status → task `failed` | task record | `aits: task ID failed: MESSAGE` on stderr | 1 |

Help for each command lists its output fields and statuses (P5); `docs/cli.md`
is generated from help (P13).

## 6. Claim, run, reclaim

**Claim**, under the lock: reclaim (below); pick the oldest `queued` task
(name order) matching `--agent`; set `running` and the lease
(`token` random, `worker_pid`, `expires_at` = now + task timeout + 1 minute,
RFC 3339 wall clock); write; unlock.

**Run**, without the lock: `shell.Submit` with the task's agent, cwd, prompt,
timeout, the worker's environment and the task's `--env` names. `Started`
takes the lock, writes the pgid into the lease if the token still matches,
and unlocks.

**Record**, under the lock: re-read the task; if the lease token is still this
worker's, write `run` and set `done` (run status `ok`) or `failed`; if not,
the task was reclaimed: write nothing, print
`aits: task ID was reclaimed after its lease expired` on stderr, exit 1.

**Reclaim** (before every `work` and `list`): under the lock, every `running`
task whose `expires_at` has passed becomes `failed` with `message`
`lease_expired`; after unlocking, for each such task with a non-zero `pgid`
whose process group still exists: SIGTERM, wait up to 5 seconds, SIGKILL
(P10). Known limits: a worker that dies between claim and agent start leaves
no pgid; a pgid can be reused by an unrelated process group in the meantime.

**Signals**: a signal before the claim gives the `stopped` error envelope,
exit 1, and touches no task. A signal while the agent runs: `shell.Submit`
returns `stopped`; the task is `failed` with that run status and its session
id; exit 1.

## 7. Guards

- **P1**: `internal/archtest` scans the imports of every `internal/<group>`
  package and fails on a cross-group edge not in the declared list, or on a
  cycle. The list, in one place: `task → shell`. `store` is not a group.
- **P7**: a structural test fails when `os.WriteFile`, `os.MkdirAll` or
  `os.Create` appears in non-test code outside `internal/store`; tests check
  the on-disk modes (0700, 0600).
- **P14**: the existing CI size check; the change is expected to add about
  40 KB.

## 8. Testing

Unit (`internal/store`, `internal/task`):
- `Dir` creates 0700 and tightens a looser existing directory; `WriteFile`
  leaves 0600 and no temp file; a second `Lock` waits for the first unlock;
- state transitions and reclaim as a table; claim order and `--agent`
  filter; a record with a foreign token is not overwritten.

Functional (`cmd/aits`, built binary, fake Claude, one shared temporary
`AITS_HOME`):
1. create → list → work → show: `done`, answer and session id; prompt on the
   agent's stdin, not in argv;
2. `work` on an empty queue: `{"status":"empty"}`, exit 0;
3. invalid `create`: exit 2, no file; `show` of an unknown id: exit 2;
4. failing agent: task `failed`, run status `agent_failed`, exit 1;
5. five tasks, five concurrent `work` calls: each task run exactly once;
6. reclaim: a hand-written `running` record with a past `expires_at` and the
   pgid of a sleeping process; `list` makes it `failed` (`lease_expired`) and
   the process is gone;
7. SIGTERM during `work`: task `failed`, run status `stopped`, session id
   kept, exit 1.

No live test: `task` only calls `shell`, which has its own.

## 9. Docs

`docs/cli.md` regenerated; README marks `aits task` available with one
example; `docs/dependencies.md` unchanged (no dependency).
