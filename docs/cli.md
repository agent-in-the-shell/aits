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
  --agent AGENT       AGENT CLI to run (required). One of: claude.
  --cwd DIR           Run the agent in DIR (default: the current directory).
  --env NAME          Pass the variable NAME from this environment to the agent. Repeatable.
  --json              Print the result, or the error, as JSON on stdout.
  --timeout DURATION  Stop the agent after DURATION, e.g. 90s or 30m. (default 30m0s)
```

## aits help

```text
Usage: aits help [flags] [COMMAND...]

Show help for aits or one of its commands.

Flags:
  --json  Print the result, or the error, as JSON on stdout.
```
