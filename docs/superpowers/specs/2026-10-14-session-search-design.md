# `aits session search` — Design

- Status: approved
- Issue: #17
- Principles: P1, P2, P3, P4, P5, P6, P7, P8, P10, P12, P13 (see `docs/principles.md`)

## 1. Goal

Find an earlier Claude Code or Codex conversation by the words in it, and get
back the `session_id` that `aits shell submit --session` (#15) continues.
Search reads the vendors' own transcript files in place; aits keeps no index
and no copy of conversation content.

## 2. Decisions

| Topic | Decision | Basis |
|---|---|---|
| Scan or index | Scan the transcript files on every query, in parallel; no index | Measured 12.3 GB / 18,696 files: 4.7 s exact, 6.6 s case-folded with 10 workers (warm cache), against 82 s for single-threaded grep. An index would be a second copy of conversation content (P8) plus a format, updates and recovery to maintain (P2, P3) |
| File discovery | `filepath.Glob` with each vendor's fixed layout | Both layouts have a fixed depth; one line per vendor, and Claude's subagent transcripts (two levels deeper) are left out by the pattern itself |
| What is searched | User and assistant message text only | Tool inputs and outputs, thinking, subagent transcripts and system-injected text hold file contents, command output and boilerplate: the likeliest place for secrets (P8) and false matches |
| Query | Whitespace-separated terms, all must occur in the session's searched text (not necessarily one message), case-insensitive | Simplest rule an agent can predict; no query language (P2) |
| Order | File modification time, newest first; that time is `updated_at` | Known without parsing; transcripts are append-only |
| Unreadable data | Counted in `skipped`, search continues | P6: never a silent partial result; one bad file must not hide all others |
| Package | New `internal/session`, no import of `internal/shell` | P1: groups stay independent; agent names are checked equal by a test instead (§7) |

## 3. Command

```
aits session search [--agent AGENT] [--cwd DIR] [--since DURATION] [--limit N] [--source KIND] [--json] TERM...
```

- `TERM...`: one or more positional arguments, split on whitespace into terms;
  at least one non-empty term.
- `--agent`: `claude` or `codex`; default both.
- `--cwd DIR`: only sessions whose working directory is `DIR` or below it;
  `DIR` is made absolute, and a session matches when its recorded directory
  is in or below `DIR` as given or `DIR` with symlinks resolved (agents
  usually record the resolved path, on macOS `/private/tmp` for `/tmp`, but
  not always). "Below" means a path-separator boundary (`/a/b` matches `/a/b`
  and `/a/b/c`, not `/a/bc`).
- `--since DURATION`: only transcript files modified within `DURATION`; must
  be positive.
- `--limit N`: at most `N` results, default 20, `N ≥ 1`.
- `--source interactive|scripted` (#24): only sessions started that way;
  default no filter. A session's kind is the first value its transcript
  records: Claude's `entrypoint` of the first event that has one (`cli`,
  `claude-vscode`, `claude-desktop` interactive; `sdk-cli`, `sdk-ts`,
  `sdk-py` scripted), Codex's `payload.source` of the first line when a
  string (`cli`, `vscode` interactive; `exec` scripted). A missing or
  unlisted value is unknown: returned without `--source`, left out by both
  values, not counted in `skipped`. An empty or other value is
  `invalid_input`. Observed with Claude Code 2.1.299 and Codex CLI 0.159.2.

Invalid input → `invalid_input`, exit 2, nothing read.

## 4. Output

`--json`:

```json
{"results":[{"agent":"claude","session_id":"…","cwd":"/home/me/src/aits","updated_at":"2026-10-13T09:12:00Z","snippet":"…"}],"skipped":{"files":0,"lines":0}}
```

- `results` is an array, `[]` when nothing matched (exit 0: no match is an
  answer).
- `updated_at`: the file's modification time, RFC 3339 in UTC.
- `snippet`: up to 160 bytes of text around the first occurrence of the first
  term in the newest message that contains it, cut on rune boundaries, with
  newlines and runs of whitespace collapsed to one space, and `…` where cut.
- `skipped.files`: transcript paths that could not be opened or whose target
  is not a regular file (a pipe, a device or a directory, which are never
  opened: a pipe would block the search and a device would never end it), and
  transcripts that hold messages but no session id or cwd; `skipped.lines`: lines in examined files
  that were not JSON or were longer than 64 MiB, among the lines read: a file
  that `--cwd` or `--source` rules out is read no further (§6). Files that hold no
  conversation (a metadata file without messages or cwd, a Codex subagent's
  rollout) are left out without being counted.

Without `--json`: per result, one line `UPDATED_AT  AGENT  SESSION_ID  CWD`
and one indented snippet line; nothing for no match. When anything was
skipped, stderr gets `aits: skipped N files and M lines that could not be
read`. Exit 0.

## 5. Vendor transcripts

| | Claude Code | Codex CLI |
|---|---|---|
| Root | `$CLAUDE_CONFIG_DIR`, default `$HOME/.claude` | `$CODEX_HOME`, default `$HOME/.codex` |
| Files | `filepath.Glob(root + "/projects/*/*.jsonl")` | `filepath.Glob(root + "/sessions/*/*/*/rollout-*.jsonl")` |
| Session id | `sessionId` of the first event that has one | `payload.id` of the first non-blank line, when it is `session_meta`; a first line that cannot be read leaves the file unidentified, counted in `skipped.files` (#23) |
| cwd | `cwd` of the first event that has one | `payload.cwd` of that same first line |
| Searched text | events with `type` `user` or `assistant`: `message.content` if a string, else the `text` of its blocks of `type` `text`; and `attachment` events with `attachment.type` `queued_command`, `commandMode` `prompt`, not `isMeta`, `origin.kind` `human` or no origin: `attachment.prompt` if a string, else the `text` of its blocks of `type` `text` (a prompt typed while the agent was working, with a pasted image); a `prompt` of another shape counts the line in `skipped.lines` (#23) | lines with `type` `response_item`, `payload.type` `message`, `payload.role` `user` or `assistant`; the `text` of `content` blocks of type `input_text` or `output_text` |
| Not searched | events with `isSidechain`, `isMeta`, `isCompactSummary`, `isVisibleInTranscriptOnly` or `isApiErrorMessage` true, or `message.model` `<synthetic>`; `tool_use`, `tool_result`, `thinking` blocks; user text that starts with `<task-notification>`, `<local-command-`, `<command-`, `<system-reminder>`, `<bash-stdout>`, `<bash-stderr>` or `[Request interrupted` | rollouts whose first line, a `session_meta`, has a `subagent` key in `payload.source` (the whole file); `developer` role, `reasoning`, `function_call`, `function_call_output`, `event_msg` lines; user text that starts with `<environment_context>`, `<user_instructions>`, `# AGENTS.md instructions`, `<recommended_plugins>`, `<codex_internal_context>`, `<subagent_notification>`, `<turn_aborted>`, `<user_action>`, `<image` or `</image>` |

"Starts with" ignores leading whitespace, and applies to Claude's queued
prompts too. Kept on purpose: Claude's `<bash-input>` (the command the user
typed after `!`) and `<pasted_content>` (text the user pasted), because their
content is the user's own.

Observed with Claude Code 2.1.296 and Codex CLI 0.159.2, and checked against
older transcripts on disk (older Codex versions wrote AGENTS.md into the first
user turn). Neither layout is a documented contract. Known limits, stated in help:

- `filepath.Glob` skips subdirectories it cannot read without reporting them,
  so those files are not counted in `skipped`.
- If a vendor changes its directory depth, that agent returns no results
  rather than an error.

A missing root or `projects`/`sessions` directory is not an error: that agent
contributes nothing.

## 6. Search

1. **List** the files for the selected agents (§5); with `--since`, drop files
   whose modification time is older.
2. **Prefilter** each file in a pool of `runtime.NumCPU()` workers: read it in
   1 MiB chunks, each chunk overlapping the previous one by the longest term's
   length minus one, lower-case it, and keep the file only if every term
   occurs. A term containing `"`, `\` or a control character is left out of
   the prefilter, because JSON escapes those bytes; the parse step decides.
   Memory in this step is bounded by workers × chunk size, whatever the file
   size (P8); the parse step below holds one line at a time, up to 64 MiB,
   per worker.
3. **Confirm** each kept file line by line: parse JSON, take the searched text
   (§5), and require every term in the session's text. Record session id,
   cwd and the snippet. Lines that are not JSON or exceed 64 MiB are counted
   and skipped.
4. **Filter** by `--cwd` and `--source`, **sort** by `updated_at` descending, ties by
   session id, **cut** to `--limit`. A session's cwd and source are its first
   recorded values, so the filter is decided as soon as its session id, cwd
   and, for `--source`, source are known: a file it rules out is not read
   further in step 3 (#51). A dropped file is never a result, so its unread
   lines cannot hide one (P6); reading them would be work for nothing (P14).

A signal stops the search between chunks and between lines (one line is
still read whole, up to the 64 MiB limit) and is reported as the `stopped`
error, never as a partial result (P6, P10 by analogy).

Search never writes under the vendor roots or `AITS_HOME`, and works with a
read-only `HOME` (P7, P12).

**Speed target.** On the measured machine, 12.3 GB with a warm cache answers
in under 10 seconds; measured once by hand and recorded in the pull request,
not asserted in CI.

## 7. Testing

**Fixtures.** Small transcript files written by the tests under a temporary
`HOME`, in both layouts, built from the shapes in §5 (no real transcript is
committed).

**Unit tests** (`internal/session`):
- each vendor's text extraction as a table, including every excluded kind;
- the prefilter across a chunk boundary, with a term split by it;
- terms with `"` or `\` still found via the parse step;
- snippet cutting (rune boundary, whitespace collapse, ellipsis);
- `--cwd` path-boundary matching.

**Functional tests** (`cmd/aits`, built binary):
- a match in each vendor, AND semantics across messages, case-insensitivity;
- `--agent`, `--cwd`, `--since` (by setting file times), `--limit`, order;
- tool output, thinking, subagent and injected text are not found;
- a non-JSON line and a file without a session id are counted in `skipped`
  and do not hide other results;
- missing vendor directories, read-only `HOME`, `CLAUDE_CONFIG_DIR` and
  `CODEX_HOME` honoured;
- invalid input → exit 2;
- the found `session_id` passed to `aits shell submit --session` produces the
  resume argv on the fake vendor;
- `--json` stdout is one object; text mode as in §4 (P5).

**P4 guard.** A test in `cmd/aits` asserts that the agent names
`internal/session` knows equal `shell.Agents()`, so the two groups keep one
vendor vocabulary without importing each other.

## 8. Docs

- `docs/cli.md` regenerated (P13); the group appears in `aits --help`.
- `aits session search --help` documents the output fields, `skipped`, what
  is and is not searched, and the known limits of §5.
- README: the group moves from planned to available, with one example.
