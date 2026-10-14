# `aits task` worker liveness — Design

- Status: approved
- Issue: #37
- Revises: `2026-10-14-task-queue-design.md` §2 (Lease, Expired lease), §3,
  §4, §6, §8
- Principles: P2, P3, P5, P6, P7, P10, P12, P13, P14 (see `docs/principles.md`)

## 1. Goal

A `running` task whose worker has died is failed by aits itself, whatever
later gets the worker's pid and without waiting out the task's timeout. A
live worker's task is never failed, even after the machine sleeps (P10).

Today a task is reclaimed only when its wall-clock lease has expired and
`kill(worker_pid, 0)` says the worker is gone. So:
- a reused pid keeps a dead worker's task `running` for as long as the new
  process lives;
- a worker that dies early holds its task for the whole timeout plus one
  minute;
- the wall clock needs special handling after sleep.

## 2. Decisions

| Topic | Decision | Basis |
|---|---|---|
| Liveness | The worker holds an exclusive `flock` on `$AITS_HOME/task/<id>.lock` from claim until its run is recorded. Reclaim try-locks it: if it gets the lock, no worker holds it. | P10: the kernel releases a `flock` when its holder exits, however it exits; no clock, no pid. P3, P14: stdlib `syscall.Flock`, the mechanism `store.Lock` already uses. |
| Probe, before design | Measured on macOS (APFS) and Linux (Docker, overlayfs), Go 1.26, with the same results: a second open in one process gets `EWOULDBLOCK`; a holder killed with SIGKILL releases the lock; an agent started with `os/exec` (`Setpgid`) does not inherit it (`O_CLOEXEC`), unless passed through `ExtraFiles`; a SIGSTOPped holder keeps it; unlinking a held lock file lets a new file at the same path be locked by another process. | P13 |
| Lock files | Never removed. One empty file per task, next to its record, which is kept as well. | Removing a held lock file splits the lock (probe). Never removing it makes that impossible by construction, with no ordering rules (P14). |
| Missing lock file | Reclaim opens only an existing lock file; only claim creates one. A `running` task whose lock file is missing is left `running` and reported on stderr. | P6: "cannot tell" is not "dead". Treating a missing file as dead would stop a live agent and lose its run. |
| Lock errors | Any try-lock error other than "held" or "missing" fails `list` and `work` (`internal`, exit 1) and changes no task. | P6 |
| Run status | `lease_expired` becomes `worker_died`, message `the worker exited before it recorded a result`. | P6: the status says what happened. P5: output shapes may change before 1.0. |
| Lease | `{"token","pgid"}`. `worker_pid`, `expires_at` and `leaseSlack` go. | P2: nothing reads them; `expires_at` is wrong after sleep. `token` still guards the record step if a lock is lost; `pgid` stops a dead worker's agent. |
| P10 | Example "an expiring lease" becomes "a lock the kernel releases when its holder exits"; guard "a test asserts an expired lease can be reclaimed" becomes "a test asserts the task of a dead worker is reclaimed, even when its pid now belongs to a live process". | Tightens P10; loosens nothing. Changed through this issue (P11). |

## 3. Components

- `internal/store`: `TryLock(path string, create bool) (unlock func(), err error)`.
  - It opens `path`, creating it with mode 0600 when `create` is true and
    tightening an existing file to 0600 then (P7). It then takes
    `LOCK_EX|LOCK_NB`.
  - Errors:
    - `ErrLocked` when another holder has the lock;
    - an error wrapping `os.ErrNotExist` when `create` is false and the file
      is missing; nothing is created;
    - otherwise the error.
  - `Lock` is unchanged.
- `internal/task`:
  - `claim` takes the task lock and returns its unlock function with the task;
  - `Work` releases it after the record step;
  - `reclaim` try-locks;
  - `alive`, `leaseSlack`, `Lease.WorkerPID` and `Lease.ExpiresAt` are removed.
  - `List` and `Work` return a `Notes{Skipped, Unknown []string}`, sorted ids,
    in place of the skipped ids alone.
- `internal/cli`: `list` and `work` print, in both modes, one stderr line per
  unknown id, after the skip lines:
  `aits: cannot tell whether the worker of task ID is alive: its lock file is missing`
  (id escaped as for skip lines). The `list --json` shape is unchanged: unknown
  ids are a diagnostic, so they go only to stderr (P5).

## 4. Flow

Locks: D is the directory lock (`store.Lock`, blocking); L is a task lock
(`store.TryLock`, never blocking).

**Claim**, under D:
1. reclaim (below);
2. pick the oldest `queued` task of the agent;
3. `TryLock(<id>.lock, create=true)`;
4. write `running` and the lease `{token, pgid: 0}`;
5. release D.

The worker keeps L. Locking before the write means there is never a
`running` task whose lock is free while its worker lives.
- If `TryLock` fails, nothing is written and `work` fails with that error.
  `ErrLocked` here means a task was re-queued by hand while its run went on.
- If the write fails, L is released and nothing changes.

**Run**, without D: `shell.Submit`.
- The agent does not inherit L.
- The `Started` callback writes the pgid under D, as today.

**Record**, under D: re-read the task.
- Token still the worker's: write `run` and `done` or `failed`.
- Otherwise: write nothing and fail with
  `task ID was reclaimed before its run was recorded`.

Then release D, then L. The unlock function is referenced until then, so the
file is not closed by a finalizer.

**Reclaim**, under D (before every `list` and `work`), for each `running`
task, `TryLock(<id>.lock, create=false)`:
- **lock obtained**: the worker is gone. Set `failed`, `lease: null`, `run`
  with status `worker_died`, its message and `finished_at`. Write the task,
  release L, and collect its non-zero `pgid` to stop after D is released
  (`shell.Stop`, as today).
- **`ErrLocked`**: a live worker holds it; leave the task.
- **missing**: leave the task `running`; add its id to `Unknown`.
- **any other error**: stop and return it. `list` and `work` fail with
  `internal`, exit 1, and no task is changed.

**Deadlock**: claim and reclaim take D then L; the worker holds L and then
waits for D in the record step. This is safe only because L is always
taken with `LOCK_NB`.

**Records written before this change** still read: unknown lease fields are
ignored, and a running record with a lease stays valid.

## 5. Known limits

- A stopped (Ctrl-Z) worker keeps its lock, so its task stays `running` until
  the worker continues or exits. This follows from "a live worker's task is
  never failed".
- `flock` on a network file system depends on the client and server. Every
  claim already depends on it through `store.Lock`. A lock error fails
  closed.
- A lock file removed by hand while its worker is also gone leaves the task
  `running`. Each `list` and `work` reports it on stderr.
- A worker of an earlier aits that is still running during an upgrade has no
  lock file. Its task is reported as unknown until that worker records its
  run.
- A worker that dies between claim and agent start leaves no pgid. A pgid
  can be reused by an unrelated group. Both are unchanged from the task-queue
  design §6.

## 6. Testing

All tests are written first and seen failing. They must pass on Linux and
macOS in CI (P13).

Unit, `internal/store` — `TestTryLock`:
- `create=true` makes a 0600 file and tightens an existing 0644 one;
- a second `TryLock` on the same path in the same process returns
  `ErrLocked`;
- after unlock it succeeds;
- `create=false` on a missing path returns an `os.ErrNotExist` error and
  creates nothing.

Unit, `internal/task`:
- **dead worker, pid alive** (replaces `TestReclaim`). A raw `running`
  record has `token`, `worker_pid` = the test's own pid and `expires_at` one
  hour ahead; its lock file exists and nobody holds it. `List` makes it
  `failed`, `worker_died`, `lease` null. This fails on main.
- **live worker past expiry** (replaces `TestReclaimSparesLiveWorker`). The
  test holds the lock: `List` leaves the task `running`. After unlock, `List`
  makes it `worker_died`.
- **missing lock file**: a `running` record with no lock file stays
  `running`; `Notes.Unknown` has its id; no lock file is created.
- **old record**: a record with `worker_pid` and `expires_at` reads without
  error.

Functional, `cmd/aits` (built binary, fake vendor):
- `TestTaskReclaim`: a hand-written `running` record with an unlocked lock
  file and the pgid of a sleeping group. `list` makes it `worker_died`, and
  the group is gone.
- `TestTaskWorkerKilled`, new:
  1. start `work --json` with the `sleep` scenario;
  2. wait until `show` has the lease pgid;
  3. SIGKILL the worker and wait for it to exit;
  4. one `list` makes the task `worker_died`, without waiting for the
     30-minute timeout, and the agent's group is gone.

  This also shows that the agent does not inherit the lock.
- `TestTaskWorkStopped`, extended. While the run is going, the lease has
  exactly the keys `token` and `pgid`, and `list` leaves the task `running`.
- **lock file removed mid-run**:
  - while `work` runs, delete `<id>.lock`. `list` exits 0, prints the unknown
    line on stderr, and the task is still `running`;
  - the worker then records `done`.
- `TestTaskLockError`, new. `<id>.lock` is a directory:
  - for a `running` task, `list --json` gives `internal`, exit 1, and leaves
    the record unchanged;
  - for a `queued` task, `work --json` gives `internal`, exit 1, and the task
    stays `queued` with `lease` null.
- `taskRecord.Lease` has only `token` and `pgid`, so strict decoding fails if
  the old fields come back.

Verification beyond tests:
- a mutation check per branch, for example reclaim creating the lock file,
  claim writing before locking, and a blocking lock in reclaim;
- an adversarial pass on kill timing and concurrency;
- the P7 structural test and the P14 size check.

## 7. Docs

- `docs/principles.md` P10 as in §2.
- The task-queue design is revised in place, with a revision line pointing
  here: §2 Lease and Expired lease rows; §3 store; §4 lease and run status;
  §6 Claim, Record, Reclaim and known limits; §8 the tests above.
- Help (`taskFields`, `list`, `work`) and the regenerated `docs/cli.md`.
