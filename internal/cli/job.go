package cli

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/agent-in-the-shell/aits/internal/job"
	"github.com/agent-in-the-shell/aits/internal/shell"
)

func jobGroup() *Command {
	return &Command{
		Name:     "job",
		Summary:  "Run scheduled commands with a timeout, a lock, a run record and alerts.",
		Details:  jobSchedule,
		Children: []*Command{jobRun(), jobList()},
	}
}

// jobSchedule says how a job is scheduled: by the OS, never by aits (#109).
const jobSchedule = `A job is any command the OS scheduler starts through aits job run: aits
runs it once and exits, and the schedule stays with cron or launchd. A
crontab that backs up notes nightly and checks prices every 5 minutes:

  MAILTO=""
  30 23 * * *  aits job run notes-backup --timeout 10m --on-alert ~/bin/notify -- ~/bin/notes-backup.sh
  */5 * * * *  aits job run prices --timeout 4m --on-alert ~/bin/notify -- ~/bin/check-prices

cron does not expand ~ in the path of a program, only the shell does: give
absolute paths in a launchd agent's ProgramArguments. A job does not get
your shell's environment (see "aits task --help").
`

// jobDirNote says where jobs live, for every job command (P5).
const jobDirNote = `Jobs live in $AITS_HOME/job (default ~/.local/share/aits/job): per job
NAME.log (rotated), NAME.json (its state) and NAME.lock. With neither
AITS_HOME nor HOME set, every job command fails (invalid_input).

`

func jobRun() *Command {
	return &Command{
		Name:    "run",
		Args:    "NAME -- COMMAND [ARG...]",
		Summary: "Run a scheduled command once: timeout, lock, log, run record, alerts.",
		Details: jobDirNote + `NAME is letters, digits, '.', '_' and '-', starting with a letter or
digit, at most 100 bytes. COMMAND is found on PATH and run in aits's working
directory, in its own process group, with stdin /dev/null and the explicit
environment shell submit gives an agent (PATH, HOME, USER, LOGNAME, LANG,
LC_*, TZ, TMPDIR and the proxy variables) plus each --env NAME. aits does not
look at what COMMAND is: a job that runs an agent calls shell submit itself.

Log: COMMAND's stdout and stderr are appended to NAME.log between a start
line and an end line (time, status, exit code, duration). The log rotates
before a run once it reaches 5 MiB; NAME.log.1 to NAME.log.3 are kept.

Overlap: while a run of NAME holds NAME.lock, another run of NAME does not
start COMMAND: it is skipped, with one line in the log, and exits 0.

Stopping: on a timeout or a signal (SIGTERM, SIGINT, SIGHUP), aits sends TERM
to the group and KILL 5s later, or at once on a second signal. Once COMMAND
exits, whatever it left running in its group is killed.

Alerts: aits decides, from NAME.json, whether a run is worth telling; the
--on-alert program, if given, delivers it. A failure is any status but ok,
skipped and stopped (a stop comes from outside, such as launchctl or a
shutdown, and is not the job's failure).
  failure     The first failure, or one that differs from the last alerted
              one: another status or exit code, or other output (numbers
              and long hex strings aside).
  repeat      The same failure, 15m, 30m, 1h, 2h and 4h after the previous
              alert, then every 6h.
  suppressed  The same failure within that time: nothing is delivered.
  recovery    The first ok run after a failure; the failure is cleared.
  none        Nothing to tell.
The program gets one JSON object on stdin:
  {name, kind, status, exit_code, message, finished_at, failing_since,
   failures, unalerted, output_tail, log}
where failures counts the failed runs since failing_since, unalerted the
failed runs since the previous alert that were not delivered, output_tail
is the last 4 KiB of the run's output, and log is NAME.log. It runs with the
same environment as COMMAND, for 30s at most, its output in the log. A
program that fails is reported in alert_error and the log; the run's status
and exit code stay COMMAND's.

Output: with --json, one run record:
  name, status, started_at, finished_at, duration_ms, alert
  exit_code    Once COMMAND ran; -1 if a signal ended it.
  message      Every status but ok: one line explaining it.
  alert_error  When the --on-alert program failed.
Without --json, "NAME  STATUS  ALERT" on stdout, and the message and the
alert error on stderr.

Statuses:
  ok            COMMAND exited 0.
  failed        COMMAND exited non-zero or was killed by a signal.
  timeout       --timeout elapsed; COMMAND was stopped.
  stopped       aits got a signal; COMMAND was stopped.
  start_failed  COMMAND could not be started, or an --env variable is not
                set. It alerts like any failure.
  skipped       The previous run still holds the lock; nothing was run.

Exit codes:
  0  The status is ok or skipped.
  1  Any other status, or the job's files could not be written.
  2  Usage error or invalid input; nothing was run.

Errors: unknown_flag, invalid_input (exit 2); internal (exit 1).
`,
		Setup: func(fs *flag.FlagSet) Runner {
			timeout := fs.Duration("timeout", shell.DefaultTimeout, "Stop COMMAND after `DURATION`, e.g. 90s or 30m.")
			pass := repeated(fs, "env", "Pass the variable `NAME` from this environment to COMMAND and to the alert program.")
			onAlert := nonEmpty(fs, "on-alert", "Deliver alerts by running `PROGRAM`, an absolute path, with the alert as JSON on stdin.", "a program")
			return func(ctx context.Context, env *Env, args []string) (int, error) {
				if len(args) < 2 {
					return 0, usageError("invalid_input", "expected NAME -- COMMAND [ARG...]")
				}
				rec, err := job.Run(ctx, job.Request{
					Name: args[0], Command: args[1:], Timeout: *timeout,
					Environ: env.Environ, Pass: *pass, OnAlert: *onAlert, Kill: env.Kill,
				})
				if err != nil {
					return 0, taskError(err)
				}
				if env.JSON {
					writeJSON(env.Stdout, rec)
				} else {
					fmt.Fprintf(env.Stdout, "%s  %s  %s\n", rec.Name, rec.Status, rec.Alert)
					if rec.Message != "" {
						fmt.Fprintf(env.Stderr, "aits: job %s %s: %s\n", rec.Name, rec.Status, printable(rec.Message))
					}
					if rec.AlertError != "" {
						fmt.Fprintf(env.Stderr, "aits: job %s: %s\n", rec.Name, printable(rec.AlertError))
					}
				}
				if rec.Status == job.StatusOK || rec.Status == job.StatusSkipped {
					return exitOK, nil
				}
				return exitFailure, nil
			}
		},
	}
}

func jobList() *Command {
	return &Command{
		Name:    "list",
		Summary: "List every job that has run: its last run, last success and failure.",
		Details: jobDirNote + `Output: one line per job, by name,
"NAME  STATUS  FINISHED_AT  LAST_OK_AT  FAILING_SINCE" (empty columns as -);
with --json {"jobs":[{name, last, last_ok_at, failing}], "skipped":[NAME]}:
  last        The last run that was not skipped: job run's record.
  last_ok_at  When the job last ran ok.
  failing     While it fails: since, failures, signature, alerts,
              last_alert_at and unalerted, as job run --help describes.
A NAME.json that cannot be read is skipped and named in "skipped" or,
without --json, on stderr. A morning check:

  aits job list --json | jq -r '.jobs[] | select(.failing) | "\(.name) \(.failing.since)"'

Exit codes:
  0  Listed.
  1  The job directory could not be read.
  2  Usage error or invalid input.

Errors: unknown_flag, invalid_input (exit 2); internal (exit 1).
`,
		Setup: func(*flag.FlagSet) Runner {
			return func(_ context.Context, env *Env, args []string) (int, error) {
				if len(args) != 0 {
					return 0, usageError("invalid_input", "list takes no arguments")
				}
				states, skipped, err := job.List(env.Environ)
				if err != nil {
					return 0, taskError(err)
				}
				if env.JSON {
					writeJSON(env.Stdout, struct {
						Jobs    []job.State `json:"jobs"`
						Skipped []string    `json:"skipped"`
					}{states, skipped})
					return exitOK, nil
				}
				for _, name := range skipped {
					fmt.Fprintf(env.Stderr, "aits: skipped job %s whose state could not be read\n", printable(name))
				}
				for _, st := range states {
					status, finished, failing := "-", "-", "-"
					if st.Last != nil {
						status, finished = string(st.Last.Status), stampOr(st.Last.FinishedAt)
					}
					if st.Failing != nil {
						failing = stampOr(st.Failing.Since)
					}
					fmt.Fprintf(env.Stdout, "%s  %s  %s  %s  %s\n", st.Name, status, finished, stampOr(st.LastOKAt), failing)
				}
				return exitOK, nil
			}
		},
	}
}

// stampOr formats t, or "-" when it is zero.
func stampOr(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format(time.RFC3339)
}
