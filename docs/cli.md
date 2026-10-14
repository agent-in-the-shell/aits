# CLI reference

Generated from the help output of `aits` by
`go test ./cmd/aits -run TestCLIDocs -update`. Do not edit by hand.

## aits

```text
Usage: aits COMMAND

Run coding agents from the command line.

Commands:
  shell    Run prompts through vendor agent CLIs.
  session  Search the coding agents' own conversation transcripts.
  task     Queue agent runs, work them one at a time, and read their results.
  help     Show help for aits or one of its commands.

Run 'aits COMMAND --help' for details.
```

## aits shell

```text
Usage: aits shell COMMAND

Run prompts through vendor agent CLIs.

Commands:
  submit  Run one prompt through an agent CLI and return the answer and session id.

Run 'aits shell COMMAND --help' for details.
```

## aits shell submit

```text
Usage: aits shell submit [flags] (PROMPT | -)

Run one prompt through an agent CLI and return the answer and session id.

Flags:
  --agent AGENT       AGENT CLI to run (required). One of: claude, codex.
  --cwd DIR           Run the agent in DIR (default: the current directory).
  --env NAME          Pass the variable NAME from this environment to the agent. Repeatable.
  --json              Print the result, or the error, as JSON on stdout.
  --session ID        Continue the conversation ID: the session_id of an earlier result.
  --timeout DURATION  Stop the agent after DURATION, e.g. 90s or 30m. (default 30m0s)

Output:
  With --json, stdout is exactly one JSON object: a result once the agent has
  started, otherwise {"error":{"code":"…","message":"…"}}. Without --json,
  stdout is the answer for status ok and empty otherwise; everything else
  goes to stderr.

Result fields:
  status       Always. One of the statuses below.
  agent        Always.
  session_id   When the agent reported one, on every status; pass it to --session.
  answer       Only for ok.
  exit_code    Always. The agent's exit code; -1 if a signal ended it.
  duration_ms  Always.
  stderr_tail  Only for agent_failed: the last 4 KiB of the agent's stderr.
  message      Every status but ok: one line explaining it.

Statuses:
  ok                The agent answered.
  agent_failed      The agent exited non-zero or reported an error.
  bad_agent_output  The agent's JSON events were not usable.
  output_too_large  One output event exceeded 16 MiB; the agent was stopped.
  timeout           --timeout elapsed; the agent was stopped.
  stopped           aits got SIGTERM, SIGINT or SIGHUP; the agent was stopped.

Errors (nothing was started):
  unknown_command     exit 2  No such command.
  unknown_flag        exit 2  No such flag for this command.
  invalid_input       exit 2  A flag value, the prompt or an argument is invalid.
  agent_not_found     exit 1  The agent CLI is not on PATH.
  agent_start_failed  exit 1  The agent CLI could not be started.
  stdin_failed        exit 1  The prompt could not be read from stdin.
  stopped             exit 1  aits got a signal before the agent started.
  internal            exit 1  An unexpected failure inside aits.

Exit codes:
  0  The status is ok.
  1  Any other status, or an error marked exit 1 above.
  2  Usage error or invalid input; nothing was started.
```

## aits session

```text
Usage: aits session COMMAND

Search the coding agents' own conversation transcripts.

Commands:
  search  Find conversations that contain every term, with an id to continue them.

Run 'aits session COMMAND --help' for details.
```

## aits session search

```text
Usage: aits session search [flags] TERM...

Find conversations that contain every term, with an id to continue them.

Flags:
  --agent AGENT     Search only AGENT's transcripts. One of: claude, codex.
  --cwd DIR         Only sessions whose working directory is DIR or below it, with or without symlinks resolved.
  --json            Print the result, or the error, as JSON on stdout.
  --limit N         Return at most N results. (default 20)
  --since DURATION  Only transcripts changed within DURATION, e.g. 72h.

Output:
  With --json, stdout is one JSON object:
  {"results":[...],"skipped":{"files":N,"lines":N}}. Without --json, each
  result is a line "UPDATED_AT  AGENT  SESSION_ID  CWD" and an indented
  snippet, and anything skipped is reported on stderr. Text output shows
  control characters from transcripts as Go escapes (\x1b, \r); --json
  carries the original text. No match is not an error.

Result fields:
  agent        The agent whose transcript matched.
  session_id   Pass it to "aits shell submit --session" to continue.
  cwd          The session's working directory, as the agent recorded it.
  updated_at   When the transcript file last changed, in UTC; newest first.
  snippet      Text around the first term in the newest message with it.

Skipped:
  files        Transcript paths that could not be read or are not regular files
               (pipes, devices, directories; never opened), and transcripts
               that hold messages but name no session or cwd.
  lines        Lines that were not JSON or were longer than 64 MiB.

Searched:
  User and assistant message text, and prompts queued while the agent was
  working; every term somewhere in the session, ignoring case. Not tool calls
  or their output, model thinking, subagent transcripts (Claude's or Codex's),
  compaction summaries, or text the agent CLI adds to a turn by itself
  (instructions such as AGENTS.md, reminders, notifications, "!" shell
  output, interruption and API-error notices).

Transcripts:
  claude  $CLAUDE_CONFIG_DIR/projects/*/*.jsonl (default ~/.claude)
  codex   $CODEX_HOME/sessions/*/*/*/rollout-*.jsonl (default ~/.codex)
  Subdirectories that cannot be read are passed over without a count; if a
  vendor changes this layout, its sessions are not found.

Exit codes:
  0  The search ran, with or without results.
  1  Stopped by a signal, or an unexpected failure.
  2  Usage error or invalid input.
```

## aits task

```text
Usage: aits task COMMAND

Queue agent runs, work them one at a time, and read their results.

Commands:
  create  Queue a prompt for an agent; prints the task id.
  list    List tasks, oldest first.
  show    Show one task and its run.
  work    Claim the oldest queued task, run it, record the result, exit.

Run 'aits task COMMAND --help' for details.
```

## aits task create

```text
Usage: aits task create [flags] (PROMPT | -)

Queue a prompt for an agent; prints the task id.

Flags:
  --agent AGENT       AGENT CLI to run (required). One of: claude, codex.
  --cwd DIR           Run the agent in DIR (default: the current directory).
  --env NAME          Pass the variable NAME from the worker's environment to the agent. Repeatable.
  --json              Print the result, or the error, as JSON on stdout.
  --timeout DURATION  Stop the agent after DURATION, e.g. 90s or 30m. (default 30m0s)

Output: the task id; with --json {"id":"…","status":"queued"}.

Exit codes:
  0  Queued.
  1  The task could not be written.
  2  Usage error or invalid input; nothing was written.

Errors: invalid_input (exit 2); stdin_failed, stopped, internal (exit 1).
```

## aits task list

```text
Usage: aits task list [flags]

List tasks, oldest first.

Flags:
  --json  Print the result, or the error, as JSON on stdout.

Output: one line per task, "ID  STATUS  AGENT  CREATED_AT"; with --json
{"tasks":[{"id","status","agent","cwd","created_at"}],"skipped":["ID"]}.
Expired leases of dead workers are reclaimed first.

A task file that cannot be read (not JSON, not a record of its own id, an
unknown status, or running without a lease) is skipped and left as it is:
its id is in "skipped" (always present, sorted) or, without --json, on
stderr as "aits: skipped task ID that could not be read". A running task
whose file cannot be read is not reclaimed, and its agent is not stopped.

Exit codes:
  0  Listed.
  1  The queue could not be read or updated.
  2  Usage error.

Errors: invalid_input (exit 2); internal (exit 1).
```

## aits task show

```text
Usage: aits task show [flags] ID

Show one task and its run.

Flags:
  --json  Print the result, or the error, as JSON on stdout.

Task fields (--json):
  id           Created by "task create".
  status       queued, running, done or failed.
  agent, cwd, prompt, timeout_ms, created_at
  env          Variable names passed from the worker's environment.
  lease        While running: token, worker_pid, pgid, expires_at.
  run          Once worked: the "shell submit" result (status, session_id,
               answer, exit_code, duration_ms, stderr_tail, message) plus
               started_at and finished_at. run.status is a shell status,
               not_started (the agent was never started; no started_at), or
               lease_expired (the worker died; set by the next work or list).

Text output: "ID  STATUS  AGENT  CWD" and, for a done task, the answer on
stdout; the session id and why a task failed on stderr.

Exit codes:
  0  The task exists, whatever its status.
  1  It could not be read.
  2  Unknown id or usage error.

Errors: invalid_input (exit 2); internal (exit 1).
```

## aits task work

```text
Usage: aits task work [flags]

Claim the oldest queued task, run it, record the result, exit.

Flags:
  --agent AGENT  Only claim tasks for AGENT.
  --json         Print the result, or the error, as JSON on stdout.

Task fields (--json):
  id           Created by "task create".
  status       queued, running, done or failed.
  agent, cwd, prompt, timeout_ms, created_at
  env          Variable names passed from the worker's environment.
  lease        While running: token, worker_pid, pgid, expires_at.
  run          Once worked: the "shell submit" result (status, session_id,
               answer, exit_code, duration_ms, stderr_tail, message) plus
               started_at and finished_at. run.status is a shell status,
               not_started (the agent was never started; no started_at), or
               lease_expired (the worker died; set by the next work or list).

Text output: "ID  STATUS  AGENT  CWD" and, for a done task, the answer on
stdout; the session id and why a task failed on stderr.

With nothing queued, work prints {"status":"empty"} with --json (nothing
otherwise) and exits 0. A worker whose lease was reclaimed records nothing
and fails with "task ID was reclaimed after its lease expired". A task file
that cannot be read is skipped and left as it is, with
"aits: skipped task ID that could not be read" on stderr.

Exit codes:
  0  The task is done, or nothing was queued.
  1  The task failed, or work could not finish.
  2  Usage error.

Errors: invalid_input (exit 2); stopped, internal (exit 1).
```

## aits help

```text
Usage: aits help [flags] [COMMAND...]

Show help for aits or one of its commands.

Flags:
  --json  Print the result, or the error, as JSON on stdout.
```
