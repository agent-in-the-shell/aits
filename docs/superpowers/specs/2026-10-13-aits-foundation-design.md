# aits foundation: principles and development workflow

- Date: 2026-10-13
- Status: approved 2026-10-13
- Scope: sub-project 1 of aits — the design principles and the repository
  foundation every later feature issue builds on. Each command group gets its
  own issue → spec → plan cycle afterwards.

## 1. What aits is

`aits` is a single Go binary for running coding agents from the command line:
dispatch work to vendor agent CLIs (Claude Code, Codex), queue and follow up
on that work, search past agent sessions, and route LLM requests through an
OpenAI-compatible gateway.

It is a clean-room, open-source implementation. An earlier private
implementation exists and is used as a reference for *behaviour and lessons
only*; no code is copied, and its legacy designs are deliberately not carried
over. The two evolve independently: aits makes no compatibility promise with
the reference, and none with itself before 1.0.

The primary caller is an AI agent; humans are supported but secondary.

## 2. Recorded decisions

| Decision | Choice | Rejected alternative (why) |
| --- | --- | --- |
| Hosting | Public repo `agent-in-the-shell/aits`; issues there | Private-first (less open process) |
| Scope | `model`, `shell`, `task`, `session` command groups; more may be added later via P1/P2 | Fewer groups (user wants the full product) |
| Relationship to reference | Independent; no compatibility | Replace-the-private-version (would import its constraints) |
| Primary caller | AI agents | Humans-first (agents would need a second interface later) |
| Packaging | One binary, subcommand groups | Separate binaries (version skew between them was a recurring problem) |
| Language | Go | — |
| Dependencies | Stdlib first, no cgo, each direct dependency justified | Free choice (dependency sprawl) |
| Principle style | Few rules, each with the failure it prevents and a mechanical guard | Long prose (prose-only rules did not stop recurring bug classes) |
| Flag placement | Flags anywhere; `--` ends flags | Flags-before-arguments only (agents habitually append `--json`) |
| Storage | One root, `$AITS_HOME`, default `~/.local/share/aits` | XDG four-way split (four roots and override orders to get right) |
| Child environment | Per-adapter allowlist plus explicit `--env NAME` | Inherit-minus-denylist (fails open for unlisted secrets) |
| Platforms | Linux and macOS, amd64 and arm64 | Windows (process-group semantics need a second implementation; no confirmed need) |
| Docs language | English | — |

## 3. Principles

Each principle states the rule, the failure it prevents, and the guard that
enforces it. A guard marked *review-only* cannot be checked mechanically and
relies on code review; every other guard is a test or CI check. Guards land
with the first code they protect — a guard over code that does not exist yet
is not required.

### P1 — One binary, command groups with a declared dependency graph

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

### P2 — Build only what has a caller today

**Rule.** Every feature traces to an issue with a concrete use case: who runs
it, with what input, which command, and what result. No reserved states, dead
flags, aliases, or config files for a single knob.

**Prevents.** Declared-but-never-set states, docs naming callers that do not
exist, and side products growing inside a core service.

**Guard.** `staticcheck` (including unused checks) in CI. Whether a feature has
a real caller is *review-only*.

### P3 — Stdlib first, no cgo

**Rule.** Builds use `CGO_ENABLED=0`. Every direct dependency is listed in
`docs/dependencies.md` with the reason it is needed. Expected early entries:
`modernc.org/sqlite` (pure-Go SQLite with FTS5) and `golang.org/x/sys`.

**Prevents.** Dependency sprawl (for example, a telemetry stack pulling in an
RPC framework for a CLI) and builds that cannot cross-compile to a static
binary.

**Guard.** CI builds with `CGO_ENABLED=0`; a check fails when `go.mod`'s direct
requirements and `docs/dependencies.md` disagree.

### P4 — One grammar

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

### P5 — Machine-readable by contract

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

### P6 — Fail closed, no zero-value success

**Rule.** Input that cannot be parsed, is outside the allowed set, or
conflicts with other input is an error (exit 2). "Cannot determine" is also
not allowed through. Every result carries an explicit `status` value; empty
output or a zero value never means success.

**Prevents.** Flags that some backends silently ignored; zero-value results
read as success; arguments parsed wrongly without an error.

**Guard.** Each feature issue's acceptance includes binary tests for its main
failure paths. Result types reject an empty `status` at construction.

### P7 — One storage root, safe writes

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

### P8 — Content and secret hygiene

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
environment, argv and logs; a test asserts oversized output is truncated.

### P9 — Safe defaults for exposure and privilege

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

### P10 — Owned processes, recoverable state

**Rule.** aits runs in the foreground and never daemonizes itself. Each child
runs in its own process group; on timeout, cancel or SIGTERM aits sends TERM,
waits a bounded grace period, sends KILL, reaps, and still prints a result —
with timeout and stop as distinct statuses. Persisted "in progress" state must
be recoverable by aits itself (for example, an expiring lease), without manual
repair. One user action is one atomic command.

**Prevents.** Orphaned agent processes; a task stuck `running` forever after
its worker dies; two-step follow-ups that race a background worker.

**Guard.** Tests with a fake vendor that spawns a grandchild assert nothing
survives a timeout or SIGTERM; a test asserts an expired lease can be
reclaimed.

### P11 — Clean-room, issue-first

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

### P12 — Test the built binary, never the user's environment

**Rule.** Functional tests build `aits` and run it. Vendors are always fakes:
fake `claude` and `codex` executables, fake upstream HTTP servers. Every test
uses a temporary `AITS_HOME` and `HOME`, no external network and no accounts.
Tests against real vendors are opt-in behind a build tag and are not part of
default CI. Internal logic and edge-case tables may still use unit tests.

**Prevents.** Tests that pass only on a developer's machine; tests that read or
damage the user's real agent state.

**Guard.** CI runs the test suite with `HOME` pointing at a read-only
directory, so any test writing to the real environment fails.

### P13 — Ship together, on supported platforms

**Rule.** One pull request delivers the code, CLI help, docs and tests for a
change. The CLI reference is generated from help, never hand-written.
Supported platforms are Linux and macOS on amd64 and arm64.

**Prevents.** Help, docs and behaviour drifting apart.

**Guard.** CI runs `go generate` and fails on any diff; the CI matrix covers
Linux and macOS.

## 4. Sub-project 1 deliverables

This sub-project produces the repository foundation only — no Go code. P2
forbids a command skeleton without a use case, so the Go module, the CLI
parser and the code-level guards (P1, P3, P4, P5, P7, P12, P13) land with the
first feature issue.

1. Public GitHub repo `agent-in-the-shell/aits`, `main` as default branch.
2. `README.md` — what aits is, status (pre-release, nothing usable yet), link
   to principles and contributing.
3. `LICENSE` — MIT, copyright "The aits Authors".
4. `docs/principles.md` — section 3 of this spec.
5. `CONTRIBUTING.md` — the issue-first workflow, clean-room rules (P11), how
   to run tests once code exists.
6. `AGENTS.md` and a `CLAUDE.md` that points to it — the entry point for
   coding agents working in the repo: read principles, follow the workflow.
7. `docs/dependencies.md` — empty table with the required columns.
8. `.github/ISSUE_TEMPLATE/feature.md` — sections: problem, use case,
   acceptance (commands, expected output, failure cases), out of scope,
   reference behaviours not carried over.
9. `.github/pull_request_template.md` — `Closes #`, commands run and results,
   principles touched.
10. `.github/workflows/pr.yml` — the P11 `Closes #` check.

Acceptance: the repo exists and is public; a test pull request without
`Closes #` fails the check and one with it passes; every file above exists
and contains no private-reference details.

## 5. Roadmap after the foundation

Each item becomes its own issue, and its own spec where the design is
non-trivial. Order follows dependency (P1) and the dispatch workflow being
usable end to end as early as possible.

1. `aits shell submit` — run one prompt through one vendor (Claude Code or
   Codex) with a fake-vendor test suite; brings the Go module, CLI grammar,
   JSON/exit-code contract, storage helper and the code-level guards.
2. `aits shell` second vendor and explicit session resume.
3. `aits task` — the queue: create, list, claim with lease, `work` running a
   task through `shell`, follow-up as one command.
4. `aits session` — import vendor transcripts, full-text search, replay.
5. `aits model serve` — OpenAI-compatible chat completions, one generic
   OpenAI-compatible upstream plus Anthropic, single dispatch loop, loopback
   and token by default.
6. Release: install channels and a single-install acceptance test.

## 6. Out of scope for aits (until an issue proves a caller)

A web portal; response caching; prompt/content logging; subscription-token
impersonation; telemetry exporters; vendor auto-install; daemon mode; Windows.
