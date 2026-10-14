package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/agent-in-the-shell/aits/internal/shell"
	"github.com/agent-in-the-shell/aits/internal/task"
)

func taskGroup() *Command {
	return &Command{
		Name:     "task",
		Summary:  "Queue agent runs, work them one at a time, and read their results.",
		Children: []*Command{taskCreate(), taskList(), taskShow(), taskWork()},
	}
}

// taskDetails documents the task record printed by show and work (P5).
const taskDetails = `Task fields (--json):
  id           Created by "task create"; ids sort by creation time.
  status       queued, running, done or failed.
  agent, cwd, prompt, timeout_ms, created_at
  env          Variable names passed from the worker's environment.
  lease        While running: token, worker_pid, pgid, expires_at.
  run          Once worked: the "shell submit" result (status, session_id,
               answer, exit_code, duration_ms, stderr_tail, message) plus
               started_at and finished_at. run.status is a shell status,
               not_started (the agent could not be started), or
               lease_expired (the worker died; set by the next work or list).

Exit codes:
  0  Done, or nothing to do.
  1  The task failed, or the command could not finish.
  2  Usage error or invalid input; nothing was written.
`

func taskCreate() *Command {
	return &Command{
		Name:    "create",
		Args:    "(PROMPT | -)",
		Summary: "Queue a prompt for an agent; prints the task id.",
		Setup: func(fs *flag.FlagSet) Runner {
			agent := fs.String("agent", "", "`AGENT` CLI to run (required). One of: "+strings.Join(shell.Agents(), ", ")+".")
			cwd := nonEmpty(fs, "cwd", "Run the agent in `DIR` (default: the current directory).", "a directory")
			timeout := fs.Duration("timeout", shell.DefaultTimeout, "Stop the agent after `DURATION`, e.g. 90s or 30m.")
			var pass []string
			fs.Func("env", "Pass the variable `NAME` from the worker's environment to the agent. Repeatable.", func(v string) error {
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
				t, err := task.Create(env.Environ, task.Spec{Agent: *agent, Cwd: dir, Prompt: prompt, Timeout: *timeout, Env: pass})
				if err != nil {
					return 0, taskError(err)
				}
				if env.JSON {
					writeJSON(env.Stdout, map[string]string{"id": t.ID, "status": string(t.Status)})
				} else {
					fmt.Fprintln(env.Stdout, t.ID)
				}
				return exitOK, nil
			}
		},
	}
}

func taskList() *Command {
	return &Command{
		Name:    "list",
		Summary: "List tasks, oldest first.",
		Setup: func(*flag.FlagSet) Runner {
			return func(_ context.Context, env *Env, args []string) (int, error) {
				if len(args) != 0 {
					return 0, usageError("invalid_input", "list takes no arguments")
				}
				tasks, err := task.List(env.Environ)
				if err != nil {
					return 0, taskError(err)
				}
				type item struct {
					ID        string      `json:"id"`
					Status    task.Status `json:"status"`
					Agent     string      `json:"agent"`
					Cwd       string      `json:"cwd"`
					CreatedAt time.Time   `json:"created_at"`
				}
				items := []item{}
				for _, t := range tasks {
					items = append(items, item{t.ID, t.Status, t.Agent, t.Cwd, t.CreatedAt})
				}
				if env.JSON {
					writeJSON(env.Stdout, map[string]any{"tasks": items})
					return exitOK, nil
				}
				for _, i := range items {
					fmt.Fprintf(env.Stdout, "%s  %s  %s  %s\n", i.ID, i.Status, i.Agent, i.CreatedAt.Format(time.RFC3339))
				}
				return exitOK, nil
			}
		},
	}
}

func taskShow() *Command {
	return &Command{
		Name:    "show",
		Args:    "ID",
		Summary: "Show one task and its run.",
		Details: taskDetails,
		Setup: func(*flag.FlagSet) Runner {
			return func(_ context.Context, env *Env, args []string) (int, error) {
				if len(args) != 1 {
					return 0, usageError("invalid_input", "expected one task id; got %d", len(args))
				}
				t, err := task.Show(env.Environ, args[0])
				if err != nil {
					return 0, taskError(err)
				}
				writeTask(env, t)
				return exitOK, nil
			}
		},
	}
}

func taskWork() *Command {
	return &Command{
		Name:    "work",
		Summary: "Claim the oldest queued task, run it, record the result, exit.",
		Details: taskDetails,
		Setup: func(fs *flag.FlagSet) Runner {
			agent := nonEmpty(fs, "agent", "Only claim tasks for `AGENT`.", "an agent (one of: "+strings.Join(shell.Agents(), ", ")+")")
			return func(ctx context.Context, env *Env, args []string) (int, error) {
				if len(args) != 0 {
					return 0, usageError("invalid_input", "work takes no arguments")
				}
				t, err := task.Work(ctx, env.Environ, *agent)
				if err != nil {
					return 0, taskError(err)
				}
				if t == nil {
					if env.JSON {
						writeJSON(env.Stdout, map[string]string{"status": "empty"})
					}
					return exitOK, nil
				}
				writeTask(env, *t)
				if t.Status != task.Done {
					return exitFailure, nil
				}
				return exitOK, nil
			}
		},
	}
}

// writeTask prints a task: the record with --json; otherwise a summary line,
// the session and the answer of a done run, or why it failed.
func writeTask(env *Env, t task.Task) {
	if env.JSON {
		writeJSON(env.Stdout, t)
		return
	}
	fmt.Fprintf(env.Stdout, "%s  %s  %s  %s\n", t.ID, t.Status, t.Agent, t.Cwd)
	if t.Run == nil {
		return
	}
	if t.Run.SessionID != "" {
		fmt.Fprintf(env.Stderr, "session: %s\n", t.Run.SessionID)
	}
	if t.Status == task.Done {
		fmt.Fprintln(env.Stdout, t.Run.Answer)
	} else {
		fmt.Fprintf(env.Stderr, "aits: task %s failed: %s: %s\n", t.ID, t.Run.Status, t.Run.Message)
	}
}

func taskError(err error) error {
	var invalid *task.InvalidError
	switch {
	case errors.As(err, &invalid):
		return usageError("invalid_input", "%s", invalid.Reason)
	case errors.Is(err, shell.ErrStopped):
		return failure("stopped", "%v", err)
	}
	return failure("internal", "%v", err)
}
