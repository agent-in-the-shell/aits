---
name: aits
description: Run Claude Code or Codex as a sub-agent from the shell, queue agent work and collect it later, or find and continue an earlier agent conversation, with the aits CLI. Use when asked to hand a prompt to another coding agent, get a second agent's review or fix, run agent jobs in the background, or resume a past Claude Code or Codex session.
---

# aits

`aits` is one binary that drives vendor coding-agent CLIs (`claude`, `codex`)
for you and reports back in JSON. It never talks to a model itself: the
agents use the user's own logins.

The installed binary's help is the authority for its version. When this
skill and `aits COMMAND --help` disagree, follow the help, and check what is
installed with `aits version`.

## Which command

| Job | Command |
| --- | --- |
| Ask an agent one thing and wait for the answer | `aits shell submit` |
| Continue that conversation | `aits shell submit --session ID` |
| Find an earlier conversation to continue | `aits session search` |
| Queue work and collect it later | `aits task create`, then `aits task work` |
| Wait for a queued task to finish | `aits task wait ID --timeout 9m` |
| Queue a follow-up in the same conversation | `aits task create --after ID` |
| See queued and finished work | `aits task list`, `aits task show ID` |
| Which aits is installed | `aits version` |
| Full help for any command | `aits help COMMAND...` or `aits COMMAND --help` |

## Always use --json

Pass `--json` on every call. stdout is then exactly one JSON object: the
result, or `{"error":{"code":"…","message":"…"}}`. Diagnostics and warnings
go to stderr; never parse stderr.

Exit codes: `0` success, `1` the operation failed, `2` usage error or invalid
input (nothing was started or written). An agent's own exit code is inside
the result as `exit_code`, never aits's exit code. Always read `status`,
not just the exit code.

Flags go before or after the prompt; `--` ends flags, so a prompt that
starts with `-` is `-- "-prompt"`. A long prompt, or one you build in a
file, goes on stdin with `-` as the prompt: `aits shell submit --agent
claude --json - < prompt.md` (at most 16 MiB). An unknown flag or command is
exit 2 (`unknown_flag`, `unknown_command`); it is never sent as a prompt.

## Run one prompt: aits shell submit

```sh
aits shell submit --agent codex --cwd ~/src/proj --json "Review internal/task for bugs; list them, change nothing"
```

```json
{"status":"ok","agent":"codex","session_id":"…","answer":"…","exit_code":0,"duration_ms":81234}
```

- `--agent` is required: `claude` or `codex`. Codex runs only inside a git
  repository.
- `--cwd DIR` is where the agent works (default: the current directory).
- `--timeout DURATION` (default `30m`) stops the agent; raise it for big jobs.
- `--env NAME` passes one variable from your environment to the agent;
  repeat it per variable. Nothing else is passed except what the vendor
  needs, so name API keys or tool settings the agent must see.
- `--session ID` continues the conversation `ID`: the `session_id` of an
  earlier result, or one from `aits session search`.
- `--write` lets the agent edit files and run commands; see below.

Without `--write` the agent can read but not change anything: Claude Code
refuses tools that need approval (listed in `denied_tools`), Codex runs
read-only. A refused tool does not change the status; the agent may have
answered another way, so read `answer` and `denied_tools` together.

### Statuses

| status | Meaning | What to do |
| --- | --- | --- |
| `ok` | The agent answered | Use `answer`; keep `session_id` to continue |
| `agent_failed` | The agent exited non-zero or reported an error | Read `message` and `stderr_tail` (login, quota, bad cwd) |
| `bad_agent_output` | The agent's output was not usable | Usually a vendor CLI version aits does not understand; report it |
| `output_too_large` | One output event exceeded 16 MiB | Ask for a shorter answer, or have the agent write a file |
| `timeout` | `--timeout` elapsed; the agent was stopped | Continue with `--session` and a narrower prompt, or raise `--timeout` |
| `stopped` | aits got a signal; the agent was stopped | Rerun or continue with `--session` |

Every status but `ok` has a one-line `message`. `session_id` is present
whenever the agent reported one, on any status, so a timed-out conversation
can still be continued.

Errors before the agent starts (no result, exit 1): `agent_not_found` (the
vendor CLI is not on PATH), `agent_start_failed`, `stdin_failed`, `stopped`,
`internal`. Exit 2: `invalid_input`, `unknown_flag`.

### Writing files: --write

`--write` lets the agent edit files and run commands in its working
directory without asking, inside its vendor's sandbox. Use it only when the
user asked for changes to be made, on that one call; it is never a default
and no variable turns it on. aits prints a warning on stderr every time,
even with `--json`; that is expected.

Inside the sandbox, writes outside the working directory (and temp
directories) fail, for example a build cache under `$HOME`. A command the
sandbox stopped is not in `denied_tools`: it looks like any failed command,
so if the agent reports permission errors, that is why. Codex keeps `.git`
read-only: commit yourself afterwards. Claude Code leaves
`.claude/.cc-writes` in the working directory.

A typical review-then-fix:

```sh
r=$(aits shell submit --agent codex --cwd ~/src/proj --json "Review internal/task for bugs")
sid=$(printf '%s' "$r" | jq -r .session_id)
aits shell submit --agent codex --cwd ~/src/proj --session "$sid" --write --json "Fix the first bug you listed and run its tests"
```

## Find a conversation: aits session search

```sh
aits session search --agent claude --cwd ~/src/proj --since 72h --json process group timeout
```

```json
{"results":[{"agent":"claude","session_id":"…","cwd":"/Users/…/proj","updated_at":"…","snippet":"…"}],"skipped":{"files":0,"lines":0}}
```

Every term must appear somewhere in the session's user and assistant text,
ignoring case; tool output and thinking are not searched. Results are newest
first, at most `--limit N` (default 20). `--source interactive` keeps
sessions a person ran; `--source scripted` keeps ones run by `claude -p` or
`codex exec`, which includes aits's own runs. No match is `"results":[]`
with exit 0, not an error. Pass a result's `session_id` and `agent` to
`aits shell submit --session` with `--cwd` set to its `cwd`.

## Queue work: aits task

A task is a stored prompt that a worker runs later. The queue lives under
`$AITS_HOME/task` (default `~/.local/share/aits/task`).

```sh
id=$(aits task create --agent claude --cwd ~/src/proj --json "Review internal/session for bugs" | jq -r .id)
next=$(aits task create --after "$id" --json "Fix the first bug you found" | jq -r .id)
aits task work --json        # runs the oldest claimable task, then exits
aits task show "$id" --json
aits task wait "$id" --timeout 9m --json   # blocks until it is done or failed
```

- `aits task create` takes the same `--agent`, `--cwd`, `--env`, `--timeout`
  and `--write` as `shell submit`, and prints `{"id":"…","status":"queued"}`.
- `--after ID` queues a follow-up that continues task `ID`'s conversation,
  with its agent and cwd, once `ID` is done. A follow-up gets write access
  only from its own `--write`, never from its parent. A task may have only
  one follow-up that has not failed.
- `aits task work` claims one task, runs it, records the result and exits.
  Run it in a loop, or in several terminals for parallel workers;
  `--agent AGENT` claims only that agent's tasks. With nothing claimable it
  prints `{"status":"empty"}` and exits 0: stop looping then. Its exit code
  is 1 when the task failed.
- `aits task list --json` lists every task oldest first; ids it could not
  read are in `"skipped"`. `aits task show ID --json` prints the whole task.
- `aits task wait ID --json` blocks until the task is `done` or `failed`,
  then prints it as `show` does, with exit 0 either way: read `status`. Use
  it instead of polling `show`, which never notices a dead worker. It starts
  no worker, so something must run `task work`. Always pass `--timeout`
  below your shell tool's own limit (Claude Code's Bash tool stops a command
  after 2 minutes by default, 10 at most); on `timeout` (exit 1) the task is
  untouched: call `wait` again. A signal ends it with `stopped`.

Task `status` is `queued`, `running`, `done` or `failed`. A worked task has
a `run`: the `shell submit` result fields plus `started_at` and
`finished_at`. `run.status` is one of the shell statuses above, or:

- `not_started`: the agent never started, or the task is a follow-up whose
  parent does not exist, failed or ended without a session.
- `worker_died`: the worker exited before recording a result. `list`,
  `work` and `wait` find this by themselves; nothing needs repairing. Create the task
  again to retry.

Only a `done` task's `run.answer` is the agent's answer; read
`run.message` for any other.

## Version: aits version

`aits version --json` prints `{"version":"…"}`, a release tag or a
pseudo-version ending in the commit. If a flag or field in this skill is
missing from the installed help, the binary is older than the skill:
upgrade with `go install github.com/agent-in-the-shell/aits/cmd/aits@latest`.
`unknown_version` (exit 1) means the binary was built without version data.

## Don'ts

- Do not parse text output or stderr; use `--json`.
- Do not treat exit 0 alone as an answer: `task work` exits 0 with
  `{"status":"empty"}`, and `session search` exits 0 with no results.
- Do not add `--write` to a run the user did not ask to make changes.
- Do not pass secrets in the prompt; pass the variable's name with `--env`.
