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

Errors: unknown_command, unknown_flag (exit 2).
```

## aits shell

```text
Usage: aits shell COMMAND

Run prompts through vendor agent CLIs.

Commands:
  submit  Run one prompt through an agent CLI and return the answer and session id.

Run 'aits shell COMMAND --help' for details.

Errors: unknown_command, unknown_flag (exit 2).
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

Prompt: PROMPT, or "-" to read it from stdin, at most 16 MiB; more is
invalid_input.

Output:
  With --json, stdout is exactly one JSON object: a result once the agent has
  started, otherwise {"error":{"code":"…","message":"…"}}. Without --json,
  stdout is the answer for status ok and empty otherwise; everything else
  goes to stderr, with control characters in the message and session id
  shown as Go escapes (\x1b, \r).

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
  timeout           --timeout elapsed before the agent exited; it was stopped.
  stopped           aits got SIGTERM, SIGINT or SIGHUP before the agent
                    exited, even after a timeout; it was stopped.

Stopping: on a timeout or a signal, aits sends TERM to the agent's process
group and KILL 5s later, or at once on a second signal. It still waits for
the group to exit and prints the result.

Errors (nothing was started):
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

Errors: unknown_command, unknown_flag (exit 2).
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
  --source KIND     Only sessions started KIND. One of: interactive, scripted (see Source below).

Output:
  With --json, stdout is exactly one JSON object:
  {"results":[...],"skipped":{"files":N,"lines":N}}, or on failure
  {"error":{"code":"…","message":"…"}}. Without --json, each
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
  lines        Lines that were not JSON or were longer than 64 MiB. A file that
               --cwd or --source rules out is read no further, so its later
               lines are not counted.

Searched:
  User and assistant message text, and prompts queued while the agent was
  working; every term somewhere in the session, ignoring case. Not tool calls
  or their output, model thinking, subagent transcripts (Claude's or Codex's),
  compaction summaries, or text the agent CLI adds to a turn by itself
  (instructions such as AGENTS.md, reminders, notifications, "!" shell
  output, interruption and API-error notices).

Source (--source):
  A session's kind is the first value its transcript records:
  claude  entrypoint of the first event that has one
            interactive  cli, claude-vscode, claude-desktop
            scripted     sdk-cli (claude -p), sdk-ts, sdk-py
  codex   payload.source of the first line (session_meta), when a string
            interactive  cli, vscode
            scripted     exec
  A session with no such value, or a value not listed, is of unknown kind:
  returned without --source, left out by both values. Observed with Claude
  Code 2.1.299 and Codex CLI 0.159.2; not a documented vendor contract.

Transcripts:
  claude  $CLAUDE_CONFIG_DIR/projects/*/*.jsonl (default ~/.claude)
  codex   $CODEX_HOME/sessions/*/*/*/rollout-*.jsonl (default ~/.codex)
  Subdirectories that cannot be read are passed over without a count; if a
  vendor changes this layout, its sessions are not found. If neither the
  variable nor HOME is set for an agent searched, the search fails
  (invalid_input).

Exit codes:
  0  The search ran, with or without results.
  1  Stopped by a signal, or an unexpected failure.
  2  Usage error or invalid input.

Errors: unknown_flag, invalid_input (exit 2); stopped, internal (exit 1).
```

## aits task

```text
Usage: aits task COMMAND

Queue agent runs, work them one at a time, and read their results.

Commands:
  create  Queue a prompt for an agent; prints the task id.
  list    List tasks, oldest first.
  show    Show one task and its run.
  work    Claim the oldest claimable task, run it, record the result, exit.

Run 'aits task COMMAND --help' for details.

Errors: unknown_command, unknown_flag (exit 2).
```

## aits task create

```text
Usage: aits task create [flags] (PROMPT | -)

Queue a prompt for an agent; prints the task id.

Flags:
  --after ID          Continue the conversation of task ID once it is done, with its agent and cwd.
  --agent AGENT       AGENT CLI to run (required unless --after). One of: claude, codex.
  --cwd DIR           Run the agent in DIR (default: the current directory, or with --after the parent's).
  --env NAME          Pass the variable NAME from the worker's environment to the agent. Repeatable.
  --json              Print the result, or the error, as JSON on stdout.
  --timeout DURATION  Stop the agent after DURATION, e.g. 90s or 30m, in whole milliseconds. (default 30m0s)

The queue is $AITS_HOME/task (default ~/.local/share/aits/task). With
neither AITS_HOME nor HOME set, every task command fails (invalid_input).

Prompt: PROMPT, or "-" to read it from stdin, at most 16 MiB; more is
invalid_input.

The prompt, --cwd and --env names must be valid UTF-8: a task is stored as
JSON text.

Output: the task id; with --json {"id":"…","status":"queued"}.

Follow-ups (--after ID): --agent and --cwd, if given, must be ID's (--cwd is
made absolute, symlinks not resolved). ID must not have failed or ended
without a session, and may have only one follow-up that has not failed (one
whose worker died counts until its agent is stopped; try again then). A
queued follow-up whose parent does not exist, failed or has no session when
done is failed by the next list or work (run status not_started); until then
show reports it queued. An unreadable task file fails create (internal).

Exit codes:
  0  Queued.
  1  The queue could not be read or written.
  2  Usage error or invalid input; nothing was written.

Errors: unknown_flag, invalid_input (exit 2); stdin_failed, stopped, internal (exit 1).
```

## aits task list

```text
Usage: aits task list [flags]

List tasks, oldest first.

Flags:
  --json  Print the result, or the error, as JSON on stdout.

The queue is $AITS_HOME/task (default ~/.local/share/aits/task). With
neither AITS_HOME nor HOME set, every task command fails (invalid_input).

Output: one line per task, "ID  STATUS  AGENT  CREATED_AT"; with --json
{"tasks":[{"id","status","agent","cwd","created_at","after"}],"skipped":["ID"]},
with after only for a follow-up. Tasks of dead workers are reclaimed first,
and queued follow-ups whose parent does not exist, failed or has no session
when done are failed (run status not_started). A running task whose lock
file is missing cannot be judged: it stays running, with
"aits: cannot tell whether the worker of task ID is alive: its lock file is missing"
on stderr.

A task file that cannot be read (not a regular file named by a task id, not
JSON, not the record of that id, an unknown status, or running without a
lease) is skipped and left as it is: its id is in "skipped" (always present,
sorted) or, without --json, on stderr as
"aits: skipped task ID that could not be read". A running task whose file
cannot be read is not reclaimed, and its agent is not stopped; a follow-up
of a task whose file cannot be read stays queued.

Exit codes:
  0  Listed.
  1  The queue could not be read or updated.
  2  Usage error or invalid input.

Errors: unknown_flag, invalid_input (exit 2); internal (exit 1).
```

## aits task show

```text
Usage: aits task show [flags] ID

Show one task and its run.

Flags:
  --json  Print the result, or the error, as JSON on stdout.

The queue is $AITS_HOME/task (default ~/.local/share/aits/task). With
neither AITS_HOME nor HOME set, every task command fails (invalid_input).

Task fields (--json):
  id           Created by "task create".
  status       queued, running, done or failed.
  agent, cwd, prompt, timeout_ms, created_at
  env          Variable names passed from the worker's environment.
  after        For a follow-up: the task whose session it continues.
  lease        While running: token, pgid and, once the agent has started,
               started_at.
  run          Once worked: the "shell submit" result (status, agent,
               session_id, answer, exit_code, duration_ms, stderr_tail,
               message) plus started_at and finished_at. run.status is a
               shell status, not_started (the agent was never started: it
               could not be started after the claim, or the task is a
               follow-up whose parent does not exist, failed or has no
               session when done), or worker_died (its worker exited first;
               set by the next work or list). A not_started or worker_died
               run has no exit_code and no duration_ms, which are unknown;
               only a worker_died run whose agent started has started_at.

Text output: "ID  STATUS  AGENT  CWD" and, for a done task, the answer on
stdout; the session id and why a task failed on stderr, with control
characters shown as Go escapes (\x1b, \r).

Exit codes:
  0  The task exists, whatever its status.
  1  It could not be read.
  2  Unknown id, usage error or invalid input.

Errors: unknown_flag, invalid_input (exit 2); internal (exit 1).
```

## aits task work

```text
Usage: aits task work [flags]

Claim the oldest claimable task, run it, record the result, exit.

Flags:
  --agent AGENT  Only claim tasks for AGENT.
  --json         Print the result, or the error, as JSON on stdout.

The queue is $AITS_HOME/task (default ~/.local/share/aits/task). With
neither AITS_HOME nor HOME set, every task command fails (invalid_input).

Task fields (--json):
  id           Created by "task create".
  status       queued, running, done or failed.
  agent, cwd, prompt, timeout_ms, created_at
  env          Variable names passed from the worker's environment.
  after        For a follow-up: the task whose session it continues.
  lease        While running: token, pgid and, once the agent has started,
               started_at.
  run          Once worked: the "shell submit" result (status, agent,
               session_id, answer, exit_code, duration_ms, stderr_tail,
               message) plus started_at and finished_at. run.status is a
               shell status, not_started (the agent was never started: it
               could not be started after the claim, or the task is a
               follow-up whose parent does not exist, failed or has no
               session when done), or worker_died (its worker exited first;
               set by the next work or list). A not_started or worker_died
               run has no exit_code and no duration_ms, which are unknown;
               only a worker_died run whose agent started has started_at.

Text output: "ID  STATUS  AGENT  CWD" and, for a done task, the answer on
stdout; the session id and why a task failed on stderr, with control
characters shown as Go escapes (\x1b, \r).

With nothing claimable (no task queued, none for AGENT with --agent, or only
follow-ups whose parent is not done or cannot be read), work exits 0,
printing {"status":"empty"} with --json and nothing otherwise. A worker whose
task was reclaimed records nothing and fails with
"task ID was reclaimed before its run was recorded". A task file that cannot
be read is skipped and left as it is, with
"aits: skipped task ID that could not be read" on stderr. Running tasks whose lock file is missing are reported as list does.
The agent is stopped as shell submit stops it. If its process group cannot
be recorded in the task, the agent is killed at once, so that it never runs
unrecorded: the run is stopped, with the reason as its message, or work
fails (internal) if the run cannot be recorded either; the next list or work
then reclaims the task.

Exit codes:
  0  The task is done, or nothing was claimable.
  1  The task failed, or work could not finish.
  2  Usage error or invalid input.

Errors: unknown_flag, invalid_input (exit 2); stopped, internal (exit 1).
```

## aits help

```text
Usage: aits help [flags] [COMMAND...]

Show help for aits or one of its commands.

Flags:
  --json  Print the result, or the error, as JSON on stdout.

Errors: unknown_command, unknown_flag (exit 2).
```
