# aits

Run coding agents from the command line: dispatch work to vendor agent CLIs,
queue and follow up on it, search past agent sessions, and route LLM requests
through an OpenAI-compatible gateway — one Go binary, built for AI agents to
call.

> **Status:** pre-release. `aits shell submit` (Claude Code and Codex, with
> `--session` to continue), `aits session search` and `aits task` work;
> `model` is planned.

## Install

    go install github.com/agent-in-the-shell/aits/cmd/aits@latest

Check which build is installed:

    aits version

prints the version Go recorded at build time, such as
`v0.0.0-20261015142825-3556be1bc9f6`, which ends in the commit.

## Usage

    aits shell submit --agent claude --cwd ~/src/proj --json "Summarize what this repo does"

prints

    {"status":"ok","agent":"claude","session_id":"…","answer":"…","exit_code":0,"duration_ms":12345}

`--agent codex` runs Codex CLI the same way; Codex only runs inside a git
repository. To continue a conversation, pass its `session_id` back:

    aits shell submit --agent claude --cwd ~/src/proj --session <session_id> --json "Now fix the first issue"

Find an earlier conversation and its id:

    aits session search --json "process group timeout"

Queue work for agents and collect it later:

    id=$(aits task create --agent claude --cwd ~/src/proj "Review internal/session for bugs")
    next=$(aits task create --after "$id" "Fix the first bug you found")  # same session, once $id is done
    aits task work            # in one or more terminals; one task per call
    aits task wait "$id" --json    # blocks until $id is done or failed

To run tasks on a schedule, let cron or launchd start `aits task create` and
`aits task work`; `aits task --help` shows how.

The full reference is [docs/cli.md](docs/cli.md).

## Agent skill

[skills/aits/SKILL.md](skills/aits/SKILL.md) teaches a coding agent when and
how to call aits. Install it where your agent loads skills, for Claude Code:

    git clone https://github.com/agent-in-the-shell/aits
    mkdir -p ~/.claude/skills && cp -R aits/skills/aits ~/.claude/skills/

Update it with the binary: each change to the CLI updates the skill in the
same commit.

## Planned command groups

| Group | Job |
| --- | --- |
| `aits shell` | Run a prompt through a vendor agent CLI (Claude Code, Codex) and return the result and session id |
| `aits task` | A local work queue: create tasks, run them through `shell`, and follow up on the same session (available) |
| `aits session` | Full-text search over vendor transcripts; get back a resumable session id (available) |
| `aits model` | OpenAI-compatible LLM gateway with routing and fallback |

## Design

- [Principles](docs/principles.md) — the rules every change follows, each with
  the failure it prevents and the check that enforces it.
- [Contributing](CONTRIBUTING.md) — issue first, one pull request per issue.

## License

MIT — see [LICENSE](LICENSE).
