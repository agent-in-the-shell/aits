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

func submitCommand() *Command {
	return &Command{
		Name:    "submit",
		Args:    "(PROMPT | -)",
		Summary: "Run one prompt through an agent CLI and return the answer and session id.",
		Setup: func(fs *flag.FlagSet) Runner {
			agent := fs.String("agent", "", "`AGENT` CLI to run (required). One of: "+strings.Join(shell.Agents(), ", ")+".")
			cwd := fs.String("cwd", "", "Run the agent in `DIR` (default: the current directory).")
			timeout := fs.Duration("timeout", shell.DefaultTimeout, "Stop the agent after `DURATION`, e.g. 90s or 30m.")
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
					Environ: env.Environ, Pass: pass,
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
