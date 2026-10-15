# `aits shell submit` — Design

- Status: approved
- Issue: #2
- Principles: P3, P4, P5, P6, P8, P9, P10, P12, P13 (see `docs/principles.md`)

## 1. Goal

Ship the first command, `aits shell submit`: hand one prompt to Claude Code,
wait for it to finish, and return the answer and the session id, as JSON when
`--json` is given. The issue's acceptance list is the behavioural contract;
this document fixes the decisions the issue leaves open.

This change also lands the Go module, the shared CLI grammar (P4), the CI test
job, and the guards for P3, P4, P5, P12 and P13.

## 2. Decisions

| Topic | Decision | Basis |
|---|---|---|
| CLI parsing | Standard library `flag`, one `FlagSet` per command, plus a small loop that allows flags anywhere and stops at `--`. No cobra. | P3: no capability is missing from the standard library; P4: no aliases, suggestions or implicit `help`/`completion` commands to switch off |
| Package layout | `cmd/aits` (main only), `internal/cli` (grammar, dispatch, help, error output, exit codes), `internal/shell` (the `shell` group: Claude adapter and child-process handling) | P1: one package per command group; P2: no separate process package before a second caller exists |
| Vendor output format | `claude -p --output-format stream-json --verbose`, read line by line | P10: the session id arrives in the first event, so failed runs remain resumable; P8: memory stays bounded per event |
| Progress | Read, not forwarded | P2: the primary caller is an agent that reads output after the command exits |
| Output limit | 16 MiB per event line, fixed, no flag | P8 bounded output; P2 no flag without a caller. The value is a judgement, not a measurement |
| `ANTHROPIC_API_KEY` | In the Claude adapter's default allowlist | P8 targets unrelated secrets; this is the credential the vendor needs |
| Fake vendor | A Go program built by the test harness, driven by a `scenario` file next to its executable | P12; environment variables cannot drive it because aits filters them (P8) |
| Live test | `cmd/aits/live_test.go` behind the `live` build tag | P12 already allows opt-in real-vendor tests behind a build tag |
| P1 import guard | Deferred to the first cross-group edge (`task` → `shell`) | With one group, the only bad edge is a cycle, which the compiler rejects |
| CLI reference | Golden-file test regenerates `docs/cli.md` with `-update` | P13; no hidden generator command in the binary (P4) |

## 3. Command

```
aits shell submit --agent claude [--cwd DIR] [--timeout DURATION] [--env NAME]... [--json] (PROMPT | -)
```

- `--agent` is required; the only accepted value is `claude`.
- `--cwd` defaults to the current directory and must be an existing directory.
- `--timeout` takes a Go duration (`90s`, `30m`), default `30m`, must be > 0.
- `--env NAME` may repeat; each passes that one variable from aits's
  environment to the child. A name that is not set in aits's environment is
  an input error (exit 2), not silently skipped.
- Exactly one positional argument: the prompt, or `-` to read it from stdin.
  An empty or all-whitespace prompt is an input error.

All input is validated before anything is started; any failure exits 2 with
`invalid_input` (or `unknown_command`, `unknown_flag`) and starts nothing.

## 4. Grammar (`internal/cli`)

- Commands form a static tree: `aits` → `shell` → `submit`. A group name alone
  prints that group's help to stdout and exits 0; `aits help` and `--help` on
  any command do the same.
- Flags may appear before, between or after positional arguments. `--`
  ends flag parsing; everything after it is positional.
- Command names come first. Before the command path reaches a leaf, only the
  global flags `--json` and `--help` are accepted (`aits --json shell submit …`
  works, `aits shell --agent claude submit …` is `unknown_flag`): the leaf's
  flags are unknown until the leaf is, so a flag value could otherwise be
  mistaken for a command name.
- `-x`/`--x` both accepted, `--name=value` and `--name value` both accepted
  (the standard library's behaviour), boolean flags take no value.
- Unknown command, unknown flag, missing flag value: exit 2. Nothing is
  guessed, no prefix matching, no "did you mean".
- Errors: with `--json` anywhere before `--`, the error is
  `{"error":{"code":"…","message":"…"}}` on stdout; otherwise
  `aits: <message>` on stderr. Exit 2 for input errors, 1 for runtime errors.

## 5. Running Claude (`internal/shell`)

**Lookup.** `claude` is resolved on the `PATH` in the request's environment.
Not found → `agent_not_found` error envelope, exit 1, nothing started.

**Argv.** `claude -p --output-format stream-json --verbose`. No permission or
model flags (P9; out of scope in the issue). The prompt is written to the
child's stdin, which is then closed; it never appears in argv (P8).

**Working directory.** `--cwd`, resolved to an absolute path.

**Environment.** Built from scratch (P8): the adapter's allowlist plus each
`--env NAME`. Claude's allowlist: `PATH`, `HOME`, `USER`, `LOGNAME`, `LANG`,
every `LC_*`, `TZ`, `TMPDIR`, `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`,
`CLAUDE_CONFIG_DIR`, `ANTHROPIC_API_KEY`. Variables not set in aits's
environment are omitted, not passed empty.

**Process group.** The child starts in its own process group. On timeout, or
when aits receives SIGTERM, SIGINT or SIGHUP, aits sends SIGTERM to the group,
waits 5 seconds, then sends SIGKILL to the group, then waits for the child
(P10). SIGHUP is included because the agent's own group never receives the
terminal's hangup. Once the agent has exited, aits waits at most 1 second more
for the copy of the prompt to its stdin, so an agent that never read its prompt
cannot hold aits open. A signal that arrives before the agent starts (for
example while the prompt is read from stdin) ends the run with the error
envelope `stopped`, exit 1, and nothing is started.

**Reading stdout.** Line by line, each line at most 16 MiB. From each event
aits keeps only the `session_id` (first one seen) and, from the event with
`"type":"result"`, its `is_error`, `result` and `session_id`. Every other
event is discarded after parsing. A line that is not JSON is
`bad_agent_output`; parsing continues after it, so a session id on a later
line is still reported (#4). A line above the limit is `output_too_large`, and the
group is terminated as for a timeout.

**Reading stderr.** Only the last 4 KiB is kept, and it is reported only for
`agent_failed`, as `stderr_tail`. aits writes
no logs.

Since #13 the event record is vendor-neutral and each agent has its own
parser; see `2026-10-14-shell-codex-design.md` §5.

## 6. Outcome

Once the child has started, the run always ends with a result object; the
error envelope is only for runs where nothing started.

A signal or timeout counts only if it arrives before the child exits. After
that, aits still stops what the child left behind (and a second signal still
kills it at once), but the outcome is the child's (#81).

| Condition (checked in this order) | `status` | aits exit |
|---|---|---|
| aits received SIGTERM/SIGINT/SIGHUP | `stopped` | 1 |
| timeout elapsed | `timeout` | 1 |
| an event line exceeded 16 MiB | `output_too_large` | 1 |
| child exited non-zero | `agent_failed` | 1 |
| a line was not JSON | `bad_agent_output` | 1 |
| result event has `is_error: true` | `agent_failed` | 1 |
| no result event, or empty `result`, or no session id | `bad_agent_output` | 1 |
| otherwise | `ok` | 0 |

Result object (`--json`):

```json
{"status":"ok","agent":"claude","session_id":"…","answer":"…","exit_code":0,"duration_ms":12345}
```

- `session_id`: present whenever any event carried one (P10), omitted
  otherwise.
- `answer`: present only for `ok`.
- `exit_code`: the child's exit code; `-1` when it was ended by a signal.
- `stderr_tail`: present only for `agent_failed`.
- `message`: a one-line human explanation, present for every status but `ok`.

Without `--json`: for `ok`, stdout is the answer text followed by a newline,
and stderr gets `session: <id>`. For any other status, stdout is empty and
stderr gets `aits: <status>: <message>` plus `session: <id>` when known.

Since #13, `message` for `agent_failed` carries the error the agent reported
when there is one, even when it exited non-zero; see
`2026-10-14-shell-codex-design.md` §5.

## 7. Testing

**Fake vendor.** `internal/shell/testdata/fakeclaude/main.go` builds to an
executable named `claude`. Each test copies it into its own temporary
directory, writes a `scenario` file beside it, and puts that directory first on
`PATH`. The fake records argv, stdin, working directory and environment to
`record.json` in the same directory. Scenarios: `ok`, `exit_nonzero`,
`is_error`, `not_json`, `no_result`, `no_session`, `huge_line`, `sleep`
(with a grandchild that also sleeps and writes its pid to a file).

**Functional tests** (`cmd/aits/e2e_test.go`). `TestMain` builds `aits` and the
fake once into a temporary directory. Every test sets a temporary `HOME` and
`AITS_HOME`. One test per acceptance item in issue #2, plus:
- grandchild pid no longer alive after `timeout` and `stopped`;
- `session_id` present on `timeout`, `stopped`, `agent_failed`,
  `output_too_large` when the fake emitted it first;
- canary variable absent from `record.json`; `--env CANARY` present.

**Unit tests** for the grammar loop and the outcome table (§6) as tables.

**Guards.**

| Principle | Guard | File |
|---|---|---|
| P3 | Every `require` in `go.mod` appears in `docs/dependencies.md`, and vice versa | `internal/archtest/deps_test.go` |
| P4 | For every command in the tree, run the binary: unknown flag → 2; flag after positional works; `--` stops flag parsing; unknown subcommand → 2 | `cmd/aits/grammar_test.go` |
| P5 | Every error and non-`ok` status, with and without `--json`: stdout is exactly one JSON object or empty, as §4 and §6 say | `cmd/aits/e2e_test.go` |
| P12 | CI sets `HOME` to a read-only directory after pointing `GOCACHE`, `GOMODCACHE` and `GOPATH` at runner temp | `.github/workflows/pr.yml` |
| P13 | `docs/cli.md` equals the help-derived golden output; `go test ./cmd/aits -run TestCLIDocs -update` regenerates it | `cmd/aits/docs_test.go` |

**Live test.** `cmd/aits/live_test.go`, `//go:build live`: real `claude`, real
`HOME`, temporary `AITS_HOME`; prompt "Reply with exactly: OK"; asserts
`status` `ok` and non-empty `session_id` and `answer`. Run with
`go test -tags live ./cmd/aits -run TestLive`. Never in CI.

**CI.** New job `test` in `pr.yml`, matrix `ubuntu-latest` and `macos-latest`:
`gofmt -l` must print nothing, `go vet ./...`, `go test ./...`. Added to the
required checks on `main`.

## 8. Docs

- `docs/cli.md`: generated reference (P13).
- `README.md`: install (`go install github.com/agent-in-the-shell/aits/cmd/aits@latest`) and one usage example.
- `docs/principles.md` P12: one sentence that opt-in live tests use the real
  `HOME` because vendor credentials live there, and keep `AITS_HOME`
  temporary.

## 9. Module

`github.com/agent-in-the-shell/aits`, `go 1.26`, no dependencies.
