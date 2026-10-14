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
const submitDetails = `Output:
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
`

func submitCommand() *Command {
	return &Command{
		Name:    "submit",
		Args:    "(PROMPT | -)",
		Summary: "Run one prompt through an agent CLI and return the answer and session id.",
		Details: submitDetails,
		Setup: func(fs *flag.FlagSet) Runner {
			agent := fs.String("agent", "", "`AGENT` CLI to run (required). One of: "+strings.Join(shell.Agents(), ", ")+".")
			cwd := fs.String("cwd", "", "Run the agent in `DIR` (default: the current directory).")
			timeout := fs.Duration("timeout", shell.DefaultTimeout, "Stop the agent after `DURATION`, e.g. 90s or 30m.")
			var session string
			fs.Func("session", "Continue the conversation `ID`: the session_id of an earlier result.", func(v string) error {
				if v == "" {
					return errors.New("needs a session id")
				}
				session = v
				return nil
			})
			var pass []string
			fs.Func("env", "Pass the variable `NAME` from this environment to the agent. Repeatable.", func(v string) error {
				pass = append(pass, v)
				return nil
			})
			return func(ctx context.Context, env *Env, args []string) (int, error) {
				if len(args) != 1 {
					return 0, usageError("invalid_input", "expected one prompt argument, or - to read it from stdin; got %d", len(args))
				}
				prompt := args[0]
				if prompt == "-" {
					var err error
					if prompt, err = readPrompt(ctx, env.Stdin); err != nil {
						return 0, err
					}
				}
				dir, err := filepath.Abs(*cwd)
				if err != nil {
					return 0, failure("internal", "resolve working directory: %v", err)
				}
				res, err := shell.Submit(ctx, shell.Request{
					Agent: *agent, Prompt: prompt, Dir: dir, Timeout: *timeout,
					Environ: env.Environ, Pass: pass, Session: session,
				})
				if err != nil {
					return 0, submitError(err)
				}
				return writeResult(env, res), nil
			}
		},
	}
}

// readPrompt reads all of r, giving up as soon as ctx is done (P10).
func readPrompt(ctx context.Context, r io.Reader) (string, error) {
	type read struct {
		b   []byte
		err error
	}
	ch := make(chan read, 1)
	go func() {
		b, err := io.ReadAll(r)
		ch <- read{b, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			return "", failure("stdin_failed", "read prompt from stdin: %v", res.err)
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

// writeResult prints a run that started and returns its exit code (P5).
func writeResult(env *Env, res shell.Result) int {
	switch {
	case env.JSON:
		writeJSON(env.Stdout, res)
	case res.Status == shell.StatusOK:
		fmt.Fprintln(env.Stdout, res.Answer)
	default:
		fmt.Fprintf(env.Stderr, "aits: %s: %s\n", res.Status, res.Message)
	}
	if !env.JSON && res.SessionID != "" {
		fmt.Fprintf(env.Stderr, "session: %s\n", res.SessionID)
	}
	if res.Status == shell.StatusOK {
		return exitOK
	}
	return exitFailure
}
