# Principles

The design rules for aits. Approved in
[the foundation spec](superpowers/specs/2026-10-13-aits-foundation-design.md);
change them only through an issue.

Each principle states the rule, the failure it prevents, and the guard that
enforces it. A guard marked *review-only* cannot be checked mechanically and
relies on code review; every other guard is a test or CI check. Guards land
with the first code they protect — a guard over code that does not exist yet
is not required.

## P1 — One binary, command groups with a declared dependency graph

**Rule.** There is one `aits` binary. Each command group (`model`, `shell`,
`task`, `session`, and any added later) is one package under
`internal/<group>`. A new group needs its own issue with a use case (P2).
Allowed dependency edges between groups are declared in one place and form a
DAG; lower-level groups (`shell`, `model`) never depend on higher-level ones
(`task`). Cross-group calls use the target group's Go API — never exec'ing
`aits` itself, never parsing another group's JSON output.

**Prevents.** Version skew between separately built tools; resolving the wrong
executable from `PATH`; one JSON shape declared twice and drifting.

**Guard.** A test scans actual imports and fails on any edge missing from the
declared list, or on a cycle.

## P2 — Build only what has a caller today

**Rule.** Every feature traces to an issue with a concrete use case: who runs
it, with what input, which command, and what result. No reserved states, dead
flags, aliases, or config files for a single knob.

**Prevents.** Declared-but-never-set states, docs naming callers that do not
exist, and side products growing inside a core service.

**Guard.** `staticcheck` (including unused checks) in CI. Whether a feature has
a real caller is *review-only*.

## P3 — Stdlib first, no cgo

**Rule.** Builds use `CGO_ENABLED=0`. Every direct dependency is listed in
`docs/dependencies.md` with the reason it is needed. Expected early entries:
`modernc.org/sqlite` (pure-Go SQLite with FTS5) and `golang.org/x/sys`.

**Prevents.** Dependency sprawl (for example, a telemetry stack pulling in an
RPC framework for a CLI) and builds that cannot cross-compile to a static
binary.

**Guard.** CI builds with `CGO_ENABLED=0`; a check fails when `go.mod`'s direct
requirements and `docs/dependencies.md` disagree.

## P4 — One grammar

**Rule.** All commands share one parser. Flags may appear before or after
positional arguments; `--` ends flag parsing. An unknown command or flag is a
usage error (exit 2) — it is never treated as a prompt or silently dropped. No
aliases, no implicit default verb. An explicit flag always beats an
environment variable. One vocabulary of vendor names across all groups.

**Prevents.** A typo sent to an agent as a prompt; an environment variable
silently overriding `--agent`; commands disagreeing on flag position and exit
codes.

**Guard.** A table-driven test walks the command tree: every command has help,
an unknown flag exits 2, and every command that produces output accepts
`--json`.

## P5 — Machine-readable by contract

**Rule.** stdout carries only the result; diagnostics go to stderr. With
`--json`, errors are also printed to stdout as
`{"error":{"code":"…","message":"…"}}` so a caller parses one stream. Exit
codes: `0` success, `1` operation failed, `2` usage error or invalid input. A
child process's own exit code is reported inside the JSON result, not passed
through as aits's exit code. Output shapes are documented in help and docs;
before 1.0 they may change.

**Prevents.** Exit code 1 meaning both "bad usage" and "the agent failed";
results that look successful but are empty.

**Guard.** Functional tests against the built binary assert stdout/stderr
separation and exit codes.

## P6 — Fail closed, no zero-value success

**Rule.** Input that cannot be parsed, is outside the allowed set, or
conflicts with other input is an error (exit 2). "Cannot determine" is also
not allowed through. Every result carries an explicit `status` value; empty
output or a zero value never means success.

**Prevents.** Flags that some backends silently ignored; zero-value results
read as success; arguments parsed wrongly without an error.

**Guard.** Each feature issue's acceptance includes binary tests for its main
failure paths. The CLI refuses to print a result with an empty `status`, and
fails with `internal` instead; tests check each place that prints one.

## P7 — One storage root, safe writes

**Rule.** All project-owned files live under one root, `$AITS_HOME` (default
`~/.local/share/aits`), with one subdirectory per command group. One function
resolves paths; there are no legacy-path fallbacks. Directories are 0700 and
files 0600, tightened on existing paths too. Writes go through temp file plus
rename. SQLite schemas are versioned with `user_version` migrations only.
Vendor-owned data (for example `~/.claude`) stays where the vendor keeps it.

**Prevents.** Several namespaces and legacy paths silently shadowing new ones;
permission fixes landing one call site at a time; two different migration
styles.

**Guard.** Tests assert actual on-disk permissions. A structural test forbids
`os.WriteFile`, `os.MkdirAll` and `os.Create` outside the storage package.

## P8 — Content and secret hygiene

**Rule.** A child process receives an explicit environment: its adapter's
allowlist plus variables the user names with `--env NAME`. Prompts are not
placed in argv when the vendor accepts stdin. Logs and diagnostics never
contain credentials or upstream response bodies. The model gateway's audit
record holds metadata only — no prompt or completion content. Captured child
output is bounded.

**Prevents.** Every API key in the parent environment reaching the child;
prompts visible in `ps`; upstream error bodies (which can echo prompt content)
written to logs; unbounded output buffered in memory.

**Guard.** Tests plant a canary secret and assert it is absent from the child
environment and argv and from aits's stdout and stderr, on success and on a
failure path; a test asserts oversized output is truncated.

## P9 — Safe defaults for exposure and privilege

**Rule.** Network listeners bind to `127.0.0.1` by default. A server refuses to
start without an auth token, and every endpoint except health requires it.
Privilege-raising flags (for example, bypassing a vendor's permission
prompts) are per-invocation only — never enabled by environment variable or
config — and print a warning on stderr. Only official, documented vendor APIs
and CLIs are used; aits never impersonates another client.

**Prevents.** Listening on all interfaces by default; unauthenticated metrics;
terms-of-service risk and constant version-chasing from impersonating
subscription clients.

**Guard.** Binary tests: `serve` without a token refuses to start; the default
bind is loopback; no environment variable enables a privilege-raising flag.

## P10 — Owned processes, recoverable state

**Rule.** aits runs in the foreground and never daemonizes itself. Each child
runs in its own process group; on timeout, cancel or SIGTERM aits sends TERM,
waits a bounded grace period, sends KILL, reaps, and still prints a result —
with timeout and stop as distinct statuses. Persisted "in progress" state must
be recoverable by aits itself (for example, a lock the kernel releases when
its holder exits), without manual repair. One user action is one atomic
command.

**Prevents.** Orphaned agent processes; a task stuck `running` forever after
its worker dies; two-step follow-ups that race a background worker.

**Guard.** Tests with a fake vendor that spawns a grandchild assert nothing
survives a timeout or SIGTERM; a test asserts the task of a dead worker is
reclaimed, even when its pid now belongs to a live process.

## P11 — Clean-room, issue-first

**Rule.** Every change starts from a self-contained public issue: the problem,
the use case, acceptance (exact commands, expected output, main failure
cases) and what is explicitly out of scope. The reference implementation
informs behaviour and lessons only; code is never copied. Public text never
contains the reference's private paths, commit IDs, file names or internal
names. An issue lists reference behaviours deliberately not carried over, and
why. One pull request per issue, with `Closes #n` in its description.

**Prevents.** Importing private code together with its legacy; leaking
private information into a public repo.

**Guard.** Issue and pull request templates; a CI check that the pull request
description contains `Closes #`. "No copied code" and "no private details" are
*review-only*.

## P12 — Test the built binary, never the user's environment

**Rule.** Functional tests build `aits` and run it. Vendors are always fakes:
fake `claude` and `codex` executables, fake upstream HTTP servers. Every test
uses a temporary `AITS_HOME` and `HOME`, no external network and no accounts.
Tests against real vendors are opt-in behind a build tag and are not part of
default CI; they use the real `HOME`, where vendor credentials live, and keep
`AITS_HOME` temporary. Internal logic and edge-case tables may still use unit
tests.

**Prevents.** Tests that pass only on a developer's machine; tests that read or
damage the user's real agent state.

**Guard.** CI runs the test suite with `HOME` pointing at a read-only
directory, so any test writing to the real environment fails.

## P13 — Ship together, on supported platforms

**Rule.** One pull request delivers the code, CLI help, docs and tests for a
change. The CLI reference is generated from help, never hand-written.
Supported platforms are Linux and macOS on amd64 and arm64.

**Prevents.** Help, docs and behaviour drifting apart.

**Guard.** `TestCLIDocs`, run by `go test ./...` in CI, fails when
`docs/cli.md` differs from the help; the CI matrix covers Linux and macOS.

## P14 — Small binary, no over-engineering

**Rule.** The release binary (`CGO_ENABLED=0 go build -trimpath
-ldflags="-s -w" ./cmd/aits`, linux/amd64) stays within the size budget in
`.github/binary-size-budget`. A change that would exceed it raises the budget
in the same pull request and says which feature needs the bytes and why a
smaller way does not work. Heavy standard-library packages (`net/http`,
`crypto/*`, `text/template`, `regexp` and the like) enter only with the
feature that needs them, inside that command group. Build the least that
meets the issue: no abstraction, option or layer before it has a caller.

**Prevents.** The binary growing quietly with every import; a heavy package
pulled in for one convenience function; general-purpose machinery nobody
asked for.

**Guard.** CI builds the release binary for linux/amd64, prints its size, and
fails when it exceeds the budget. Whether code is bigger than its job is
*review-only*.
