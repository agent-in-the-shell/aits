# `--write` — Design

- Status: approved
- Issue: #104
- Revises: `2026-10-13-shell-submit-design.md` (Argv),
  `2026-10-14-shell-codex-design.md` §2 (Codex flags),
  `2026-10-14-task-queue-design.md` §2 (Permissions),
  `2026-10-15-task-follow-up-design.md` §2 (Prompt, timeout, `--env`)
- Principles: P2, P4, P5, P6, P8, P9, P12, P13, P14 (see `docs/principles.md`)

## 1. Goal

An agent run by aits can edit files and run commands in its working
directory when the caller asks for it, one invocation at a time, inside the
vendor's own sandbox. Without the flag nothing changes for Claude Code, and
Codex is held to read-only, a resumed session too.

## 2. Decisions

| Topic | Decision | Basis |
|---|---|---|
| Levels | One boolean flag, `--write`: edit files and run commands. No edit-only level | P2: every caller (notes jobs, task workers fixing code) needs both; P6: Codex has no edit-only mode, and Claude Code's `acceptEdits` also runs `rm`, `mv`, `sed` in the working directory, so a narrower name would not be true |
| Name | `--write`, and `write` in the task record | P4: the flag and the field share a name, as `--after` and `after` do |
| Claude Code | `--permission-mode acceptEdits --settings '{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true,"allowUnsandboxedCommands":false,"failIfUnavailable":true}}'`, before `--resume` | §3. `allowUnsandboxedCommands:false`: a command the sandbox stops is not retried outside it. `failIfUnavailable:true`: no sandbox, no run (P6) |
| Codex | Always `-s`: `workspace-write` with `--write`, `read-only` without, before `resume` | §3: a session resumed with no `-s` keeps the sandbox it started with, so a session started with write access would keep it (P9). `-s` after `resume` is a usage error. One rule for new and resumed runs (P14) |
| Network | None opened | No caller needs it; both sandboxes block it by default |
| Warning | `aits: warning: --write lets the agent edit files and run commands in its working directory without asking` on stderr, also with `--json`. `shell submit` prints it once the agent starts (`Request.Started`), so invalid input and a missing agent print only their error. `task create` prints it once the task is stored | P9 requires a warning; P5 puts diagnostics on stderr and keeps stdout one JSON object, as `task list --json` already does with its notes |
| Tasks | `task create --write` stores `"write": true`; `task work` runs the task with it and prints no warning; `task work` has no `--write` | P9: the flag is per invocation. `create --write` is that invocation; the record carries it for that one run, and no environment variable or config sets it. The warning goes with the invocation that grants the right |
| Follow-ups | The follow-up's own `--write`, never the parent's | Both vendors take the permission from each invocation (§3), so leaving it out only narrows. Same rule as timeout and `--env` (P2) |
| Errors | None new: `--write=abc` is `invalid_input` from the flag parser | P14 |

## 3. Observed behaviour

Claude Code 2.1.301 and codex-cli 0.159.2 on macOS, each in a fresh git
repository.

| | Claude Code | Codex |
|---|---|---|
| Default (no flag) | `Write` and most `Bash` refused, listed in `permission_denials` | read-only sandbox; `approval_policy` is always `never` in `exec` |
| File tools may write | the working directory; elsewhere refused (`permission_denials`) | (no separate file tools) |
| Commands may write | the working directory and `/tmp/claude-<uid>`, its `$TMPDIR` | the working directory, `/tmp` and `$TMPDIR`; `.git`, `.codex`, `.agents` stay read-only |
| Network from commands | blocked by its proxy (`blocked-by-allowlist`) | blocked (DNS fails) |
| A command the sandbox stops | non-zero exit in the tool result; not in `permission_denials` | missing from the `--json` stream, or an ordinary failed `command_execution` |
| Resumed with the flag | takes the mode of the new invocation | `-s` before `resume` sets it, up or down |
| Resumed without the flag | the default mode | keeps the session's sandbox |
| Process exit code | 0 | 0 |

A write under `$HOME` outside the working directory failed with `Operation
not permitted` for both. So does any command that writes a cache there (for
example a Go build cache); a caller points it at the temp directory with
`--env`.

On Linux, Claude Code's sandbox needs bubblewrap; with `failIfUnavailable` a
run without it fails instead of running unconfined. This is the vendor's
documented behaviour, not observed here.

## 4. Changes

- `internal/shell`: `Request.Write`; each agent's `argv(session, write)`
  builds its arguments (the table's `args` and `resume` fields go).
- `internal/cli`: `--write` on `shell submit` and `task create`; one
  `writeDetails` help section for both; the warning.
- `internal/task`: `Task.Write`, `Spec.Write`, passed to the request.

## 5. Tests

- Binary tests with the fake vendors: the exact argv per agent, new and
  resumed, with and without `--write`, from `shell submit` and `task work`;
  the warning in both modes and its absence on invalid input or a missing
  agent (`TestOutputChannels`); `--write=abc`; environment variables
  changing nothing; the record's `write`, absent without the flag;
  follow-ups with and without it; `task work --write` is `unknown_flag`.
- `TestTaskHelpDocumentsFields`: every task record field is named in the
  help of `show` and `work`.
- Opt-in live test `TestLiveWrite` (`-tags live`): checks files, not answers.

## 6. Not done

- Reporting commands the sandbox stopped. Neither vendor marks them reliably.
- Network access, extra writable directories, tool allow or deny lists, and
  bypassing the vendors' checks.
- Ignoring the user's own vendor settings: they still apply.
- Commits by the agent. Codex keeps `.git` read-only; the caller commits.
