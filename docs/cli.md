# CLI reference

Generated from the help output of `aits` by
`go test ./cmd/aits -run TestCLIDocs -update`. Do not edit by hand.

## aits

```text
Usage: aits COMMAND

Run coding agents from the command line.

Commands:
  shell  Run prompts through vendor agent CLIs.
  help   Show help for aits or one of its commands.

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

## aits help

```text
Usage: aits help [flags] [COMMAND...]

Show help for aits or one of its commands.

Flags:
  --json  Print the result, or the error, as JSON on stdout.
```
