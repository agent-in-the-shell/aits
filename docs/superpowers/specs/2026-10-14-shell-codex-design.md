# `aits shell submit --agent codex` — Design

- Status: draft
- Issue: #13
- Builds on: `2026-10-13-shell-submit-design.md` (#2). Everything that spec
  says about the grammar, process group, timeout, stop, output limit, result
  object and error envelope applies unchanged; this document records only
  what is new or different.
- Principles: P2, P6, P8, P9, P10, P12, P13 (see `docs/principles.md`)

## 1. Goal

Add Codex CLI as a second agent of `aits shell submit`, with the same command,
result object, statuses and exit codes as Claude Code. Introduce the vendor
abstraction this needs, and nothing more: resuming a session is a separate
issue.

## 2. Decisions

| Topic | Decision | Basis |
|---|---|---|
| Vendor abstraction | One `agent` struct per vendor in a table, with a `parse` function field; one shared line reader, process supervisor and outcome table | P2: the abstraction appears with its second caller; an interface or per-vendor packages add indirection two vendors do not need (P1, P2) |
| Codex invocation | `codex exec --json -`, prompt on stdin, cwd set by aits | P8 (prompt not in argv); `-C` is redundant with the child's cwd |
| Codex flags | None beyond the above: no sandbox, approval, model or `--skip-git-repo-check` | P9: aits does not loosen a vendor's own safety checks; outside a git repository the run fails with Codex's message |
| Answer | The `text` of the last `agent_message` item of the turn | Observed format (§4); earlier messages in a turn are progress, the last one is the reply |
| Failure message | When the agent reported an error, `message` carries it (first line, at most 200 bytes) even if the exit code is non-zero | Today a non-zero exit hides the vendor's reason: Claude not logged in exits 1 with an empty stderr and the reason only in its result event |
| Fake vendor | One fake program that speaks Claude or Codex according to its executable name | P12; shared scenarios stay in one place and run for both agents |

## 3. Running Codex

**Lookup.** `codex` on aits's `PATH`; not found → `agent_not_found`, as for
Claude.

**Argv.** `codex exec --json -`. Nothing else.

**Environment.** Every agent gets the base allowlist: `PATH`, `HOME`, `USER`,
`LOGNAME`, `LANG`, every `LC_*`, `TZ`, `TMPDIR`, `HTTPS_PROXY`, `HTTP_PROXY`,
`NO_PROXY`. On top of it each agent lists its own variables:

| Agent | Own variables |
|---|---|
| `claude` | `CLAUDE_CONFIG_DIR`, `ANTHROPIC_API_KEY` |
| `codex` | `CODEX_HOME`, `OPENAI_API_KEY` |

Neither agent receives the other's variables. `--env NAME` works as before.

**stderr.** Codex writes log lines to stderr when it fails. They are treated
like Claude's: only the last 4 KiB is kept, reported only for `agent_failed`.

## 4. Codex output

Observed with Codex CLI 0.159.2. A first turn and a resumed turn have the same
shape, and resuming keeps the `thread_id`:

```
{"type":"thread.started","thread_id":"…"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"OK"}}
{"type":"turn.completed","usage":{…}}
```

A failed turn emits zero or more `{"type":"error","message":…}` retry notices,
then `{"type":"turn.failed","error":{"message":…}}`, and exits 1. Outside a
git repository Codex writes nothing to stdout, prints
`Not inside a trusted directory and --skip-git-repo-check was not specified.`
on stderr and exits 1.

| Event | aits records |
|---|---|
| `thread.started` | `thread_id` as the session id (first one wins) |
| `item.completed`, `item.type` = `agent_message` | `item.text` as the answer, replacing any earlier one |
| `turn.completed` | the turn finished |
| `turn.failed` | the turn finished and failed; `error.message` is the failure |
| anything else (`turn.started`, `error`, other item types) | nothing |

An event that is valid JSON but has the wrong shape for its type (for example
`item.text` not a string) makes the line unusable, as for Claude.

## 5. Shared reader and outcome

Each line is still read once, at most 16 MiB, and handed to the agent's
`parse` function, which updates one vendor-neutral record:

| Field | Meaning |
|---|---|
| `sessionID` | first session id seen |
| `answer` | the reply text, if any |
| `done` | the vendor's final event was seen (Claude `result`, Codex `turn.completed` / `turn.failed`) |
| `failed`, `failure` | the vendor reported an error, and its message |
| `notJSON`, `tooLarge` | as before |

Claude maps its `result` event to `done`, `failed` = `is_error`, and
`result` to `answer` (or to `failure` when `is_error`).

The outcome order of the #2 spec §6 is unchanged, stated on the neutral
record:

| Condition (in this order) | `status` |
|---|---|
| stopped by signal | `stopped` |
| timeout | `timeout` |
| a line over 16 MiB | `output_too_large` |
| non-zero exit | `agent_failed` |
| a line that is not usable JSON | `bad_agent_output` |
| `failed` | `agent_failed` |
| not `done`, or empty `answer`, or no session id | `bad_agent_output` |
| otherwise | `ok` |

`message` for `agent_failed`: the first line of `failure` when there is one,
otherwise `the agent exited with code N`. This changes the message, not the
status, of a Claude run that reports an error and exits non-zero.

## 6. Testing

**Fake vendor.** `internal/shell/testdata/fakeclaude` becomes
`internal/shell/testdata/fakevendor`. It reads its dialect from the base name
of its executable (`claude` or `codex`) and its scenario from the `scenario`
file beside it, and records argv, stdin, working directory and environment as
today. `TestMain` builds it once; `vendor(t, agent, scenario)` hard-links it
into the test's directory under the agent's name.

**Shared scenarios**, emitted in each dialect: `ok`, `exit_nonzero`,
`agent_error` (Claude `is_error`, Codex `turn.failed`), `not_json`,
`no_result`, `empty_result`, `no_session`, `huge_line`, `sleep`,
`ignore_term`, `linger`, `ignore_stdin`. The functional and lifecycle tests
that use them run once per agent.

**Codex-only scenarios:** `several_messages` (answer is the last),
`retry_errors` (`error` events then success → `ok`), `failed_exit0`
(`turn.failed`, exit 0 → `agent_failed`), `not_git` (stderr message, exit 1
→ `agent_failed` with it in `stderr_tail`).

**Assertions specific to this issue:**
- Codex argv is exactly `exec --json -`; the prompt is never in argv.
- Codex's environment has `CODEX_HOME` and `OPENAI_API_KEY` when set, and not
  `ANTHROPIC_API_KEY`; Claude's has the reverse.
- `agent_failed` from Claude's `is_error` with exit 1 carries the vendor's
  message, not `exited with code 1`.

**Unit tests.** One table per parse function; the `decide` table moves to the
neutral record.

**Live test.** `TestLiveCodex` behind the `live` build tag: real `codex`, real
`HOME`, a temporary directory with `git init`, prompt
`Reply with exactly: OK`; asserts `ok` with non-empty `answer` and
`session_id`. Never in CI.

## 7. Docs

- `--agent` help lists `claude, codex` (from the agent table); `docs/cli.md`
  regenerated (P13).
- Help text for `bad_agent_output` reads "The agent's JSON events were not
  usable." instead of naming stream-json.
- `2026-10-13-shell-submit-design.md` §5 and §6 point to this spec for the
  vendor-neutral record and the failure message.
- README usage mentions `--agent codex`.
