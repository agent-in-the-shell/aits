package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/agent-in-the-shell/aits/internal/shell"
)

func shellGroup() *Command {
	return &Command{
		Name:     "shell",
		Summary:  "Run prompts through vendor agent CLIs.",
		Children: []*Command{submitCommand()},
	}
}

// submitDetails documents the output of "aits shell submit" (P5).
// promptDetails documents the prompt argument of shell submit and task create.
const promptDetails = `Prompt: PROMPT, or "-" to read it from stdin, at most 16 MiB; more is
invalid_input.

`

const submitDetails = promptDetails + `Output:
  With --json, stdout is exactly one JSON object: a result once the agent has
  started, otherwise {"error":{"code":"…","message":"…"}}. Without --json,
  stdout is the answer for status ok and empty otherwise; everything else
  goes to stderr, with control characters in the message, session id and
  denied tools shown as Go escapes (\x1b, \r).

Result fields:
  status       Always. One of the statuses below.
  agent        Always.
  session_id   When the agent reported one, on every status; pass it to --session.
  answer       Only for ok.
  exit_code    Always. The agent's exit code; -1 if a signal ended it.
  duration_ms  Always.
  stderr_tail  Only for agent_failed: the last 4 KiB of the agent's stderr.
  message      Every status but ok: one line explaining it.
  denied_tools Claude Code only, on every status: the tool of each call
               refused for want of your approval, in order.
               claude -p never stops to ask, and a refusal leaves the
               status as it is: the agent may have finished another way.

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
` + writeDetails + `
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
`

func submitCommand() *Command {
	return &Command{
		Name:    "submit",
		Args:    "(PROMPT | -)",
		Summary: "Run one prompt through an agent CLI and return the answer and session id.",
		Details: submitDetails,
		Setup: func(fs *flag.FlagSet) Runner {
			agent := fs.String("agent", "", "`AGENT` CLI to run (required). One of: "+strings.Join(shell.Agents(), ", ")+".")
			cwd := nonEmpty(fs, "cwd", "Run the agent in `DIR` (default: the current directory).", "a directory")
			timeout := fs.Duration("timeout", shell.DefaultTimeout, "Stop the agent after `DURATION`, e.g. 90s or 30m.")
			session := nonEmpty(fs, "session", "Continue the conversation `ID`: the session_id of an earlier result.", "a session id")
			pass := repeated(fs, "env", "Pass the variable `NAME` from this environment to the agent.")
			write := fs.Bool("write", false, writeUsage)
			return func(ctx context.Context, env *Env, args []string) (int, error) {
				prompt, dir, err := promptArgs(ctx, env, args, *cwd)
				if err != nil {
					return 0, err
				}
				req := shell.Request{
					Agent: *agent, Prompt: prompt, Dir: dir, Timeout: *timeout,
					Environ: env.Environ, Pass: *pass, Session: *session, Write: *write, Kill: env.Kill,
				}
				if *write {
					// Once the agent has started: invalid input prints only its error.
					req.Started = func(int) error { warnWrite(env); return nil }
				}
				res, err := shell.Submit(ctx, req)
				if err != nil {
					return 0, submitError(err)
				}
				return writeResult(env, res)
			}
		},
	}
}

// promptArgs takes the one prompt argument, read from stdin when it is "-",
// and makes cwd absolute (shell submit, task create).
func promptArgs(ctx context.Context, env *Env, args []string, cwd string) (prompt, dir string, err error) {
	if len(args) != 1 {
		return "", "", usageError("invalid_input", "expected one prompt argument, or - to read it from stdin; got %d", len(args))
	}
	if prompt = args[0]; prompt == "-" {
		if prompt, err = readPrompt(ctx, env.Stdin); err != nil {
			return "", "", err
		}
	}
	if dir, err = filepath.Abs(cwd); err != nil {
		return "", "", failure("internal", "resolve working directory: %v", err)
	}
	return prompt, dir, nil
}

// maxPrompt bounds a prompt read from stdin (P8): far above any agent's
// context window, so a larger one is a mistake, such as the wrong file piped
// in. It matches the bound on one output event.
const maxPrompt = 16 << 20

// readPrompt reads all of r, at most maxPrompt bytes, giving up as soon as ctx
// is done (P10). More is invalid input: reading stops one byte past the bound.
func readPrompt(ctx context.Context, r io.Reader) (string, error) {
	type read struct {
		b   []byte
		err error
	}
	ch := make(chan read, 1)
	go func() {
		b, err := io.ReadAll(io.LimitReader(r, maxPrompt+1))
		ch <- read{b, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			return "", failure("stdin_failed", "read prompt from stdin: %v", res.err)
		}
		if len(res.b) > maxPrompt {
			return "", usageError("invalid_input", "the prompt from stdin is over 16 MiB")
		}
		return string(res.b), nil
	case <-ctx.Done():
		return "", submitError(shell.ErrStopped)
	}
}

func submitError(err error) error {
	var invalid *shell.InvalidRequestError
	switch {
	case errors.As(err, &invalid):
		return usageError("invalid_input", "%s", invalid.Reason)
	case errors.Is(err, shell.ErrStopped):
		return failure("stopped", "%v", err)
	case errors.Is(err, shell.ErrAgentNotFound):
		return failure("agent_not_found", "%v", err)
	default:
		return failure("agent_start_failed", "%v", err)
	}
}

// writeDetails documents --write, for shell submit and task create (#104).
const writeDetails = `
Write (--write): the agent edits files and runs commands without asking,
inside its vendor's sandbox; aits opens no network, and your own vendor
settings still apply. shell submit (once the agent starts) and task create
(once the task is stored) print on stderr, with --json too:
  ` + writeWarning + `

  claude  --permission-mode acceptEdits and a sandbox in --settings: file
          tools write in the working directory; commands write there and
          in Claude Code's temp directory.
  codex   -s workspace-write: commands write in the working directory, /tmp
          and $TMPDIR; .git stays read-only. Without --write codex gets
          -s read-only, a resumed session too.
A write anywhere else fails, such as a build cache under $HOME. A command the
sandbox stops is not in denied_tools: it looks like any failed command.
Claude Code leaves .claude/.cc-writes in the working directory.
`

// writeUsage is the help of --write, for shell submit and task create.
const writeUsage = "Let the agent edit files and run commands in its working directory without asking, inside its vendor's sandbox."

// writeWarning is printed on stderr, in both output modes, by an invocation
// that gives --write (P9).
const writeWarning = "aits: warning: --write lets the agent edit files and run commands in its working directory without asking"

func warnWrite(env *Env) { fmt.Fprintln(env.Stderr, writeWarning) }

// writeRunNotes prints, in text mode, the run's session id and the tools the
// agent was refused, if any, on stderr.
func writeRunNotes(env *Env, res shell.Result) {
	if res.SessionID != "" {
		fmt.Fprintf(env.Stderr, "session: %s\n", printable(res.SessionID))
	}
	if len(res.DeniedTools) > 0 {
		fmt.Fprintf(env.Stderr, "denied_tools: %s\n", printable(strings.Join(res.DeniedTools, ", ")))
	}
}

// writeResult prints a run that started and returns its exit code (P5). A
// result without a status, which would read as nothing happened, is refused
// (P6).
func writeResult(env *Env, res shell.Result) (int, error) {
	if res.Status == "" {
		return 0, failure("internal", "the run ended without a status")
	}
	switch {
	case env.JSON:
		writeJSON(env.Stdout, res)
	case res.Status == shell.StatusOK:
		fmt.Fprintln(env.Stdout, res.Answer)
	default:
		fmt.Fprintf(env.Stderr, "aits: %s: %s\n", res.Status, printable(res.Message))
	}
	if !env.JSON {
		writeRunNotes(env, res)
	}
	if res.Status == shell.StatusOK {
		return exitOK, nil
	}
	return exitFailure, nil
}
