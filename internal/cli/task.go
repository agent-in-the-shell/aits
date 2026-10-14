package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
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

// taskFields documents the task record printed by show and work (P5).
const taskFields = `Task fields (--json):
  id           Created by "task create".
  status       queued, running, done or failed.
  agent, cwd, prompt, timeout_ms, created_at
  env          Variable names passed from the worker's environment.
  lease        While running: token, worker_pid, pgid, expires_at.
  run          Once worked: the "shell submit" result (status, session_id,
               answer, exit_code, duration_ms, stderr_tail, message) plus
               started_at and finished_at. run.status is a shell status,
               not_started (the agent was never started; no started_at), or
               lease_expired (the worker died; set by the next work or list).

Text output: "ID  STATUS  AGENT  CWD" and, for a done task, the answer on
stdout; the session id and why a task failed on stderr.

Errors: invalid_input (exit 2); stdin_failed, stopped, internal (exit 1).
`

func taskCreate() *Command {
	return &Command{
		Name:    "create",
		Args:    "(PROMPT | -)",
		Summary: "Queue a prompt for an agent; prints the task id.",
		Details: `Output: the task id; with --json {"id":"…","status":"queued"}.

Exit codes:
  0  Queued.
  1  The task could not be written.
  2  Usage error or invalid input; nothing was written.
`,
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
				prompt, dir, err := promptArgs(ctx, env, args, *cwd)
				if err != nil {
					return 0, err
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
		Details: `Output: one line per task, "ID  STATUS  AGENT  CREATED_AT"; with --json
{"tasks":[{"id","status","agent","cwd","created_at"}]}. Expired leases of
dead workers are reclaimed first.

Exit codes:
  0  Listed.
  1  The tasks could not be read.
  2  Usage error.
`,
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
		Details: taskFields + `
Exit codes:
  0  The task exists, whatever its status.
  1  It could not be read.
  2  Unknown id or usage error.
`,
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
		Details: taskFields + `
With nothing queued, work prints {"status":"empty"} with --json (nothing
otherwise) and exits 0. A worker whose lease was reclaimed records nothing
and fails with "task ID was reclaimed after its lease expired".

Exit codes:
  0  The task is done, or nothing was queued.
  1  The task failed, or work could not finish.
  2  Usage error.
`,
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
	var invalid *shell.InvalidRequestError
	if errors.As(err, &invalid) || errors.Is(err, shell.ErrStopped) {
		return submitError(err)
	}
	return failure("internal", "%v", err)
}
