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
| Queue a continuation of any session, e.g. a failed round's | `aits task create --session ID` |
| See queued and finished work | `aits task list`, `aits task show ID` |
| Withdraw a queued task before a worker takes it | `aits task cancel ID` |
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
- `--model MODEL` runs the agent with that model, named as its vendor names
  it (`sonnet`, `opus` for Claude Code; a model id for Codex). Without it the
  vendor's default runs. aits does not check the name: one the vendor
  rejects is `agent_failed`, with the vendor's message in `stderr_tail`.
- `--write` lets the agent edit files and run commands; see below.
- `--allow-tool TOOL` (Claude Code only, repeatable) lets the agent run that
  one tool without asking, in Claude Code's syntax: `Read`,
  `"Bash(git diff:*)"`, `mcp__server__tool`. Every other call that needs
  approval is still refused and listed in `denied_tools`. Without `--write`
  an allowed command runs outside any sandbox, with the network. It prints a
  warning on stderr per tool; with codex it is `invalid_input`.

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

- `aits task create` takes the same `--agent`, `--cwd`, `--env`, `--timeout`,
  `--model`, `--write` and `--allow-tool` as `shell submit`, and prints
  `{"id":"…","status":"queued"}`.
- `--after ID` queues a follow-up that continues task `ID`'s conversation,
  with its agent and cwd, once `ID` is done. A follow-up gets write access
  only from its own `--write` and allowed tools only from its own
  `--allow-tool`, never from its parent; it keeps its parent's
  model unless it gives `--model`. A task may have only
  one follow-up that has not failed.
- `--session ID` queues a task that continues session `ID`, as
  `shell submit --session` does: a failed round's `run.session_id`, or one
  found with `session search`. `--agent` is required; pass the session's
  `--cwd`. It is claimable at once and takes `--model`, `--write` and
  `--allow-tool` only from its own flags. Not with `--after`. While another
  queued or running task continues `ID`, create refuses it
  (`invalid_input`, naming that task): one session, one writer.
- `aits task work` claims one task, runs it, records the result and exits.
  Run it in a loop, or in several terminals for parallel workers;
  `--agent AGENT` claims only that agent's tasks. With nothing claimable it
  prints `{"status":"empty"}` and exits 0: stop looping then. Its exit code
  is 1 when the task failed.
- `aits task list --json` lists every task oldest first, each with its `run`
  (`status`, `session_id`, `started_at`, `finished_at`, `message`; no
  `answer`) once it has one; ids it could not read are in `"skipped"`.
  `--status STATUS` (`queued`, `running`, `done`, `failed`) keeps only those.
  `aits task show ID --json` prints the whole task, with the answer.
- `aits task wait ID --json` blocks until the task is `done` or `failed`,
  then prints it as `show` does, with exit 0 either way: read `status`. Use
  it instead of polling `show`, which never notices a dead worker. It starts
  no worker, so something must run `task work`. Always pass `--timeout`
  below your shell tool's own limit (Claude Code's Bash tool stops a command
  after 2 minutes by default, 10 at most); on `timeout` (exit 1) the task is
  untouched: call `wait` again. A signal ends it with `stopped`.
- `aits task cancel ID --json` withdraws a `queued` task that has the wrong
  prompt, `--cwd` or `--write`: it becomes `failed` with run status
  `cancelled`, its queued follow-up fails, and its session is free for the
  corrected task. A `running`, `done` or `failed` task is refused
  (`invalid_input`); cancel does not stop a running agent.

Task `status` is `queued`, `running`, `done` or `failed`. A worked task has
a `run`: the `shell submit` result fields plus `started_at` and
`finished_at`. `run.status` is one of the shell statuses above, or:

- `not_started`: the agent never started, or the task is a follow-up whose
  parent does not exist, failed or ended without a session.
- `worker_died`: the worker exited before recording a result. `list`,
  `work` and `wait` find this by themselves; nothing needs repairing. Create the task
  again to retry.
- `cancelled`: withdrawn with `aits task cancel` before any worker claimed
  it; the agent never ran.

Only a `done` task's `run.answer` is the agent's answer; read
`run.message` for any other.

### Running on a schedule

To run agent work at set times or keep draining the queue with no terminal
open, have the OS scheduler start aits: aits never stays in the background,
and has no schedule of its own. See `aits task --help`.

```sh
# crontab -e
0 9 * * 1-5  aits task create --agent claude --cwd ~/src/proj "Review the commits since yesterday for bugs"
*/5 * * * *  aits task work --json >> ~/aits-work.log
```

On macOS prefer a launchd agent, which never starts a job again while it
is running:

```xml
<!-- ~/Library/LaunchAgents/local.aits-work.plist; launchctl load it -->
<plist version="1.0"><dict>
  <key>Label</key><string>local.aits-work</string>
  <key>ProgramArguments</key>
  <array><string>/opt/homebrew/bin/aits</string><string>task</string><string>work</string><string>--json</string></array>
  <key>StartInterval</key><integer>300</integer>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>/opt/homebrew/bin:/usr/bin:/bin</string></dict>
  <key>StandardOutPath</key><string>/Users/YOU/aits-work.log</string>
</dict></plist>
```

- A scheduled job gets a bare environment. `PATH` must reach `aits` and the
  agent CLI, or the task fails with run status `not_started`. `HOME` must be
  the user's (the agent's login, and the queue unless `AITS_HOME` is set);
  `create` and `work` must use the same queue.
- `--env NAME` is read from the worker's environment: set the variable in the
  `work` job, not the `create` job.
- An idle run prints `{"status":"empty"}` and exits 0. Overlapping cron runs
  claim different tasks, so slow tasks can pile up workers; use launchd, or
  `flock -n` on Linux, to keep one.
- Check what failed, and why, in one call:
  `aits task list --status failed --json | jq -r '.tasks[] | "\(.id) \(.run.status) \(.run.finished_at)"'`.
  `not_started` lines point at the job's `PATH`; continue `timeout` ones with
  `task create --session`.

## Run a loop: implement, check, fix, review, stop

aits runs one iteration well; the loop is yours. Before starting, know your
check (a command whose result you read, such as the tests) and your stop
condition (a clean review, or a round limit). A worker must be running
(`aits task work` in a loop elsewhere, or the scheduler); the agents work in
a separate tree so the user's checkout is untouched.

```sh
git -C ~/src/proj worktree add ../proj-x -b feat/x; wt=~/src/proj-x

# wait_task ID: the task once it ends, waiting again while wait times out
wait_task() {
  while t=$(aits task wait "$1" --timeout 9m --json); [ "$(printf '%s' "$t" | jq -r '.error.code // ""')" = timeout ]; do :; done
  printf '%s' "$t"
}

id=$(aits task create --agent claude --cwd "$wt" --write --json "Add X. Run go test ./... before you finish." | jq -r .id)
for round in 1 2 3 4 5; do
  [ "$(wait_task "$id" | jq -r .status)" = done ] || break        # failed: see below
  if ! out=$(cd "$wt" && go test ./... 2>&1); then                 # your check, not the agent's word
    fix=$(printf 'The tests fail:\n%s\nFix them.' "$(printf '%s' "$out" | tail -n 200)")
  else
    rid=$(aits task create --agent codex --cwd "$wt" --json "Review the uncommitted changes for bugs. Reply exactly LGTM if there are none." | jq -r .id)
    review=$(wait_task "$rid" | jq -r '.run.answer // ""')
    [ "$review" = LGTM ] && break                                  # stop: clean review
    fix=$(printf 'A reviewer found:\n%s\nFix what is right, then run the tests.' "$review")
  fi
  [ "$round" = 5 ] && break                                        # stop: round limit
  id=$(printf '%s\n' "$fix" | aits task create --after "$id" --write --json - | jq -r .id)
done
```

- **Check yourself.** Run the tests or lint in the tree and read the result;
  an agent's "tests pass" is not a check.
- **Review apart.** A new task, so a fresh session, without `--write`,
  ideally by the other agent. Pass its findings to the implementer as a
  follow-up.
- **Stop.** Always cap the rounds, and report how the loop ended: clean,
  round limit, or a failure.
- **After a failure.** `--after` follows only a `done` task with a session. If
  a round fails with `timeout` or `agent_failed`, read `run.message`, then
  continue its `run.session_id`, when it has one, with
  `aits task create --session` and `task wait` (or, in a script,
  `aits shell submit --session`); without a session id, queue a new task
  that says what happened. A `worker_died` round's agent may still be
  running: continue its session only once you have checked that no agent of
  it runs, or queue a new task instead.
- **One tree per loop.** Parallel loops each get their own `git worktree`
  and `--cwd`; never two writing agents in one tree.
- **Blocking is fine in a script.** A script with no time limit can use
  `aits shell submit --session` for each round and skip the queue. An agent
  whose shell tool times out uses `task create` and `task wait` as above.
- **Commit yourself** once the loop ends, after reading the diff: Codex keeps
  `.git` read-only, and the loop's result is the user's to accept.

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
- Do not `--allow-tool` a tool the user did not ask for, and never a bare
  `Bash` or `Bash(*)`: name the one command, such as `"Bash(curl:*)"`.
- Do not pass secrets in the prompt; pass the variable's name with `--env`.
