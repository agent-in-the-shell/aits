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

// names collects the values of a repeatable flag.
type names []string

func (n *names) String() string     { return strings.Join(*n, ",") }
func (n *names) Set(v string) error { *n = append(*n, v); return nil }

func submitCommand() *Command {
	return &Command{
		Name:    "submit",
		Args:    "(PROMPT | -)",
		Summary: "Run one prompt through an agent CLI and return the answer and session id.",
		Setup: func(fs *flag.FlagSet) Runner {
			agent := fs.String("agent", "", "`AGENT` CLI to run (required). One of: "+strings.Join(shell.Agents(), ", ")+".")
			cwd := fs.String("cwd", "", "Run the agent in `DIR` (default: the current directory).")
			timeout := fs.Duration("timeout", shell.DefaultTimeout, "Stop the agent after `DURATION`, e.g. 90s or 30m.")
			var pass names
			fs.Var(&pass, "env", "Pass the variable `NAME` from this environment to the agent. Repeatable.")
			return func(ctx context.Context, env *Env, args []string) (int, error) {
				if len(args) != 1 {
					return 0, usageError("invalid_input", "expected one prompt argument, or - to read it from stdin; got %d", len(args))
				}
				prompt := args[0]
				if prompt == "-" {
					b, err := io.ReadAll(env.Stdin)
					if err != nil {
						return 0, failure("stdin_failed", "read prompt from stdin: %v", err)
					}
					prompt = string(b)
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

func submitError(err error) error {
	var invalid *shell.InvalidRequestError
	switch {
	case errors.As(err, &invalid):
		return usageError("invalid_input", "%s", invalid.Reason)
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
