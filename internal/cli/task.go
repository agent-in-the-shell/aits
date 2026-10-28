package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/agent-in-the-shell/aits/internal/shell"
	"github.com/agent-in-the-shell/aits/internal/task"
)

func taskGroup() *Command {
	return &Command{
		Name:     "task",
		Summary:  "Queue agent runs, work them one at a time, and read their results.",
		Details:  taskSchedule,
		Children: []*Command{taskCreate(), taskList(), taskShow(), taskWait(), taskWork()},
	}
}

// taskSchedule says how to run the queue from the OS scheduler: aits itself
// never stays in the background (P10, #109).
const taskSchedule = `Running on a schedule: let the OS scheduler start aits; aits never stays
in the background. A crontab that queues a review each weekday at 09:00 and
works the queue every 5 minutes:

  0 9 * * 1-5  aits task create --agent claude --cwd ~/src/proj "Review the commits since yesterday for bugs"
  */5 * * * *  aits task work --json >> ~/aits-work.log

work exits 0 with {"status":"empty"} when nothing is claimable, so a run
with nothing to do is harmless. Runs that overlap claim different tasks,
never the same one, so a slow task can leave several workers running at
once; to keep one, start work from a scheduler that does not overlap a job
(launchd does not), or wrap it, e.g. flock -n on Linux.

On macOS use a launchd agent in ~/Library/LaunchAgents: ProgramArguments
for the command, StartInterval 300 for work or StartCalendarInterval for
create, and EnvironmentVariables for what the job needs.

A scheduled job does not get your shell's environment:
  PATH   must reach aits and the agent CLI (claude, codex): cron's default
         PATH has neither if they live in ~/.local/bin or Homebrew. Without
         the agent, the task fails with run status not_started.
  HOME   must be yours: the agent's login lives there, and the queue too
         unless AITS_HOME is set; create and work must use the same queue.
  --env  names variables that work passes to the agent from the worker's
         own environment, so set them in the work job, not the create job.
`

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
  session      Set by create --session: the session it continues.
  model        Set by create --model, or taken from the parent of a
               follow-up: the model the agent runs with.
  write        Set by create --write: the agent runs in its vendor's
               sandbox and may write without asking.
  allow_tools  Set by create --allow-tool: the tools the agent runs
               without asking.
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
		Details: taskQueue + promptDetails + `The prompt, --cwd, --model, --allow-tool and --env names must be valid
UTF-8: a task is stored as JSON text.

Output: the task id; with --json {"id":"…","status":"queued"}.

Follow-ups (--after ID): --agent and --cwd, if given, must be ID's (--cwd is
made absolute, symlinks not resolved). ID must not have failed or ended
without a session, and may have only one follow-up that has not failed (one
whose worker died counts until its agent is stopped; try again then). A
queued follow-up whose parent does not exist, failed or has no session when
done is failed by the next list or work (run status not_started); until then
show reports it queued. An unreadable task file fails create (internal).

A follow-up runs with its parent's model unless it gives --model, which
applies to the follow-up only.

Continuing a session (--session ID): work runs the task as shell submit
--session ID does, in the same conversation, so a failed round or a session
found with session search can be continued through the queue. --agent is
required; --cwd defaults to the current directory, and should be the
session's (the vendor reports a session it cannot find). It has no parent:
it is claimable at once, and takes no --model, --write or --allow-tool from
anything. --after cannot be combined with it. While another task that is
queued or running continues ID (by --session, or as a follow-up of a task
done in ID), or one that failed is still having its agent stopped, create
refuses it: two agents continuing one conversation at once leave it
undefined. aits cannot see an agent it did not start: continue a
worker_died round only once no agent of it runs, since its agent may still
be running while its worker is gone.

With --write, work runs the task as shell submit --write does, without the
warning. A follow-up takes its own --write, never its parent's: without it,
the agent resumes without write access. --allow-tool is the same: work
grants the tools without warning again, and a follow-up only its own.
` + modelDetails + writeDetails + allowToolDetails + `
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
			session := nonEmpty(fs, "session", "Continue the conversation `ID`: a session_id of an earlier result or of session search.", "a session id")
			timeout := fs.Duration("timeout", shell.DefaultTimeout, "Stop the agent after `DURATION`, e.g. 90s or 30m, in whole milliseconds.")
			model := nonEmpty(fs, "model", modelUsage, "a model name")
			pass := repeated(fs, "env", "Pass the variable `NAME` from the worker's environment to the agent.")
			write := fs.Bool("write", false, writeUsage)
			allow := repeated(fs, "allow-tool", allowToolUsage)
			return func(ctx context.Context, env *Env, args []string) (int, error) {
				prompt, dir, err := promptArgs(ctx, env, args, *cwd)
				if err != nil {
					return 0, err
				}
				if *after != "" && *cwd == "" {
					dir = "" // the parent's
				}
				t, err := task.Create(env.Environ, task.Spec{Agent: *agent, Cwd: dir, Prompt: prompt, After: *after, Session: *session, Model: *model, Timeout: *timeout, Env: *pass, Write: *write, AllowTools: *allow})
				if err != nil {
					return 0, taskError(err)
				}
				warnGrants(env, t.Write, t.AllowTools)
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
		Details: taskQueue + `Output: one line per task, "ID  STATUS  RUN_STATUS  AGENT  CREATED_AT"
(RUN_STATUS empty for a task with no run); with --json
{"tasks":[{"id","status","agent","cwd","created_at","after","session","run"}],"skipped":["ID"]},
with after only for a follow-up, session only for a --session task, and run
only once a task has one: its status, session_id, started_at, finished_at
and message as in show, without the answer (read it with show).

--status STATUS keeps only the tasks with that status (queued, running,
done or failed); skipped is not filtered. A morning check of a scheduled
queue:

  aits task list --status failed --json |
    jq -r '.tasks[] | "\(.id) \(.run.status) \(.run.finished_at)"'
 Tasks of dead workers are reclaimed first,
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
		Setup: func(fs *flag.FlagSet) Runner {
			var status task.Status
			fs.Func("status", "Show only tasks whose status is `STATUS`: queued, running, done or failed.", func(s string) error {
				status = task.Status(s)
				if !slices.Contains(task.Statuses(), status) {
					return fmt.Errorf("needs one of: %s", strings.Join(statusNames(), ", "))
				}
				return nil
			})
			return func(_ context.Context, env *Env, args []string) (int, error) {
				if len(args) != 0 {
					return 0, usageError("invalid_input", "list takes no arguments")
				}
				tasks, notes, err := task.List(env.Environ)
				if err != nil {
					return 0, taskError(err)
				}
				// The run as list shows it: show's fields without the answer
				// and the agent's output, so the list stays small (#125).
				type run struct {
					Status     shell.Status `json:"status"`
					SessionID  string       `json:"session_id,omitempty"`
					StartedAt  time.Time    `json:"started_at,omitzero"`
					FinishedAt time.Time    `json:"finished_at"`
					Message    string       `json:"message,omitempty"`
				}
				type item struct {
					ID        string      `json:"id"`
					Status    task.Status `json:"status"`
					Agent     string      `json:"agent"`
					Cwd       string      `json:"cwd"`
					CreatedAt time.Time   `json:"created_at"`
					After     string      `json:"after,omitempty"`
					Session   string      `json:"session,omitempty"`
					Run       *run        `json:"run,omitempty"`
				}
				items := []item{}
				for _, t := range tasks {
					if status != "" && t.Status != status {
						continue
					}
					i := item{t.ID, t.Status, t.Agent, t.Cwd, t.CreatedAt, t.After, t.Session, nil}
					if r := t.Run; r != nil {
						i.Run = &run{r.Status, r.SessionID, r.StartedAt, r.FinishedAt, r.Message}
					}
					items = append(items, i)
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
					var rs shell.Status
					if i.Run != nil {
						rs = i.Run.Status
					}
					fmt.Fprintf(env.Stdout, "%s  %s  %s  %s  %s\n", i.ID, i.Status, printable(string(rs)), i.Agent, i.CreatedAt.Format(time.RFC3339))
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

func taskWait() *Command {
	return &Command{
		Name:    "wait",
		Args:    "ID",
		Summary: "Wait until a task is done or failed, then show it.",
		Details: taskQueue + taskFields + `
wait looks at the queue every 250ms and reclaims as list does, so a task
whose worker died ends failed (worker_died), and a follow-up whose parent
does not exist, failed or has no session when done ends failed
(not_started), instead of being waited for. It starts no worker: a task
nobody works is waited for until --timeout. It never takes or creates a
task's lock file. A running task whose lock file is missing is reported once,
as list reports it, and waited for.

Pass a --timeout below the limit of whatever runs wait, such as an agent's
shell tool, and call wait again while the task is still running.

Exit codes:
  0  The task is done or failed: read its status.
  1  The timeout elapsed, wait was stopped, or the queue could not be read.
  2  Unknown id, usage error or invalid input.

Errors: unknown_flag, invalid_input (exit 2); timeout, stopped, internal (exit 1).
`,
		Setup: func(fs *flag.FlagSet) Runner {
			timeout := fs.Duration("timeout", 0, "Give up after `DURATION`, e.g. 90s or 9m, leaving the task as it is (default: wait until it ends).")
			return func(ctx context.Context, env *Env, args []string) (int, error) {
				if len(args) != 1 {
					return 0, usageError("invalid_input", "expected one task id; got %d", len(args))
				}
				id := args[0]
				t, err := task.Wait(ctx, env.Environ, id, *timeout, func() { reportNotes(env, task.Notes{Unknown: []string{id}}) })
				var late *task.TimeoutError
				switch {
				case errors.As(err, &late):
					return 0, failure("timeout", "%v", late)
				case errors.Is(err, context.Canceled):
					return 0, failure("stopped", "stopped while waiting for task %s", printable(id))
				case err != nil:
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

// statusNames is task.Statuses as strings, for messages.
func statusNames() []string {
	var names []string
	for _, s := range task.Statuses() {
		names = append(names, string(s))
	}
	return names
}
