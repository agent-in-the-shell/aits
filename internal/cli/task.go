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

// taskQueue says where the queue lives, for every task command (P5, #98).
const taskQueue = `The queue is $AITS_HOME/task (default ~/.local/share/aits/task). With
neither AITS_HOME nor HOME set, every task command fails (invalid_input).

`

// taskFields documents the task record printed by show and work (P5).
const taskFields = `Task fields (--json):
  id           Created by "task create".
  status       queued, running, done or failed.
  agent, cwd, prompt, timeout_ms, created_at
  env          Variable names passed from the worker's environment.
  after        For a follow-up: the task whose session it continues.
  lease        While running: token, pgid and, once the agent has started,
               started_at.
  run          Once worked: the result fields of "shell submit --help"
               plus started_at and finished_at. run.status is a shell
               status, not_started (the agent was never started: it could
               not be started after the claim, or the task is a follow-up
               whose parent does not exist, failed or has no session when
               done), or worker_died (its worker exited first; set by the
               next work or list). A not_started or worker_died
               run has no exit_code and no duration_ms, which are unknown;
               only a worker_died run whose agent started has started_at.

Text output: "ID  STATUS  AGENT  CWD" and, for a done task, the answer on
stdout; the session id, the denied tools and why a task failed on stderr,
with control characters shown as Go escapes (\x1b, \r).
`

func taskCreate() *Command {
	return &Command{
		Name:    "create",
		Args:    "(PROMPT | -)",
		Summary: "Queue a prompt for an agent; prints the task id.",
		Details: taskQueue + promptDetails + `The prompt, --cwd and --env names must be valid UTF-8: a task is stored as
JSON text.

Output: the task id; with --json {"id":"…","status":"queued"}.

Follow-ups (--after ID): --agent and --cwd, if given, must be ID's (--cwd is
made absolute, symlinks not resolved). ID must not have failed or ended
without a session, and may have only one follow-up that has not failed (one
whose worker died counts until its agent is stopped; try again then). A
queued follow-up whose parent does not exist, failed or has no session when
done is failed by the next list or work (run status not_started); until then
show reports it queued. An unreadable task file fails create (internal).

Exit codes:
  0  Queued.
  1  The queue could not be read or written.
  2  Usage error or invalid input; nothing was written.

Errors: unknown_flag, invalid_input (exit 2); stdin_failed, stopped, internal (exit 1).
`,
		Setup: func(fs *flag.FlagSet) Runner {
			agent := fs.String("agent", "", "`AGENT` CLI to run (required unless --after). One of: "+strings.Join(shell.Agents(), ", ")+".")
			cwd := nonEmpty(fs, "cwd", "Run the agent in `DIR` (default: the current directory, or with --after the parent's).", "a directory")
			after := nonEmpty(fs, "after", "Continue the conversation of task `ID` once it is done, with its agent and cwd.", "a task id")
			timeout := fs.Duration("timeout", shell.DefaultTimeout, "Stop the agent after `DURATION`, e.g. 90s or 30m, in whole milliseconds.")
			pass := repeated(fs, "env", "Pass the variable `NAME` from the worker's environment to the agent.")
			return func(ctx context.Context, env *Env, args []string) (int, error) {
				prompt, dir, err := promptArgs(ctx, env, args, *cwd)
				if err != nil {
					return 0, err
				}
				if *after != "" && *cwd == "" {
					dir = "" // the parent's
				}
				t, err := task.Create(env.Environ, task.Spec{Agent: *agent, Cwd: dir, Prompt: prompt, After: *after, Timeout: *timeout, Env: *pass})
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
		Details: taskQueue + `Output: one line per task, "ID  STATUS  AGENT  CREATED_AT"; with --json
{"tasks":[{"id","status","agent","cwd","created_at","after"}],"skipped":["ID"]},
with after only for a follow-up. Tasks of dead workers are reclaimed first,
and queued follow-ups whose parent does not exist, failed or has no session
when done are failed (run status not_started). A running task whose lock
file is missing cannot be judged: it stays running, with
"aits: cannot tell whether the worker of task ID is alive: its lock file is missing"
on stderr.

A task file that cannot be read (not a regular file named by a task id, not
JSON, not the record of that id, an unknown status, or running without a
lease) is skipped and left as it is: its id is in "skipped" (always present,
sorted) or, without --json, on stderr as
"aits: skipped task ID that could not be read". A running task whose file
cannot be read is not reclaimed, and its agent is not stopped; a follow-up
of a task whose file cannot be read stays queued.

Exit codes:
  0  Listed.
  1  The queue could not be read or updated.
  2  Usage error or invalid input.

Errors: unknown_flag, invalid_input (exit 2); internal (exit 1).
`,
		Setup: func(*flag.FlagSet) Runner {
			return func(_ context.Context, env *Env, args []string) (int, error) {
				if len(args) != 0 {
					return 0, usageError("invalid_input", "list takes no arguments")
				}
				tasks, notes, err := task.List(env.Environ)
				if err != nil {
					return 0, taskError(err)
				}
				type item struct {
					ID        string      `json:"id"`
					Status    task.Status `json:"status"`
					Agent     string      `json:"agent"`
					Cwd       string      `json:"cwd"`
					CreatedAt time.Time   `json:"created_at"`
					After     string      `json:"after,omitempty"`
				}
				items := []item{}
				for _, t := range tasks {
					items = append(items, item{t.ID, t.Status, t.Agent, t.Cwd, t.CreatedAt, t.After})
				}
				if env.JSON {
					reportNotes(env, task.Notes{Unknown: notes.Unknown}) // skipped ids are in the JSON
					writeJSON(env.Stdout, struct {
						Tasks   []item   `json:"tasks"`
						Skipped []string `json:"skipped"`
					}{items, append([]string{}, notes.Skipped...)})
					return exitOK, nil
				}
				reportNotes(env, notes)
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
		Details: taskQueue + taskFields + `
Exit codes:
  0  The task exists, whatever its status.
  1  It could not be read.
  2  Unknown id, usage error or invalid input.

Errors: unknown_flag, invalid_input (exit 2); internal (exit 1).
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
				if err := writeTask(env, t); err != nil {
					return 0, err
				}
				return exitOK, nil
			}
		},
	}
}

func taskWork() *Command {
	return &Command{
		Name:    "work",
		Summary: "Claim the oldest claimable task, run it, record the result, exit.",
		Details: taskQueue + taskFields + `
With nothing claimable (no task queued, none for AGENT with --agent, or only
follow-ups whose parent is not done or cannot be read), work exits 0,
printing {"status":"empty"} with --json and nothing otherwise. A worker whose
task was reclaimed records nothing and fails with
"task ID was reclaimed before its run was recorded". A task file that cannot
be read is skipped and left as it is, with
"aits: skipped task ID that could not be read" on stderr. Running tasks whose lock file is missing are reported as list does.
The agent is stopped as shell submit stops it. If its process group cannot
be recorded in the task, the agent is killed at once, so that it never runs
unrecorded: the run is stopped, with the reason as its message, or work
fails (internal) if the run cannot be recorded either; the next list or work
then reclaims the task.

Exit codes:
  0  The task is done, or nothing was claimable.
  1  The task failed, or work could not finish.
  2  Usage error or invalid input.

Errors: unknown_flag, invalid_input (exit 2); stopped, internal (exit 1).
`,
		Setup: func(fs *flag.FlagSet) Runner {
			agent := nonEmpty(fs, "agent", "Only claim tasks for `AGENT`.", "an agent (one of: "+strings.Join(shell.Agents(), ", ")+")")
			return func(ctx context.Context, env *Env, args []string) (int, error) {
				if len(args) != 0 {
					return 0, usageError("invalid_input", "work takes no arguments")
				}
				t, notes, err := task.Work(ctx, env.Kill, env.Environ, *agent)
				reportNotes(env, notes)
				if err != nil {
					return 0, taskError(err)
				}
				if t == nil {
					if env.JSON {
						writeJSON(env.Stdout, map[string]string{"status": "empty"})
					}
					return exitOK, nil
				}
				if err := writeTask(env, *t); err != nil {
					return 0, err
				}
				if t.Status != task.Done {
					return exitFailure, nil
				}
				return exitOK, nil
			}
		},
	}
}

// writeTask prints a task: the record with --json; otherwise a summary line,
// the session and the answer of a done run, or why it failed. A run without a
// status is refused (P6).
func writeTask(env *Env, t task.Task) error {
	if t.Run != nil && t.Run.Status == "" {
		return failure("internal", "task %s has a run without a status", t.ID)
	}
	if env.JSON {
		writeJSON(env.Stdout, t)
		return nil
	}
	fmt.Fprintf(env.Stdout, "%s  %s  %s  %s\n", t.ID, t.Status, t.Agent, t.Cwd)
	if t.Run == nil {
		return nil
	}
	writeRunNotes(env, t.Run.Result)
	if t.Status == task.Done {
		fmt.Fprintln(env.Stdout, t.Run.Answer)
	} else {
		fmt.Fprintf(env.Stderr, "aits: task %s failed: %s: %s\n", t.ID, t.Run.Status, printable(t.Run.Message))
	}
	return nil
}

// reportNotes names, on stderr, each task file that could not be read and
// each running task whose lock file is missing, so that whether its worker is
// alive cannot be told.
func reportNotes(env *Env, n task.Notes) {
	for _, id := range n.Skipped {
		fmt.Fprintf(env.Stderr, "aits: skipped task %s that could not be read\n", printable(id))
	}
	for _, id := range n.Unknown {
		fmt.Fprintf(env.Stderr, "aits: cannot tell whether the worker of task %s is alive: its lock file is missing\n", printable(id))
	}
}

func taskError(err error) error {
	var invalid *shell.InvalidRequestError
	switch {
	case errors.As(err, &invalid):
		return usageError("invalid_input", "%s", invalid.Reason)
	case errors.Is(err, shell.ErrStopped):
		return failure("stopped", "%v", err)
	}
	return failure("internal", "%v", err)
}
