package cli

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/agent-in-the-shell/aits/internal/job"
	"github.com/agent-in-the-shell/aits/internal/shell"
)

func jobGroup() *Command {
	return &Command{
		Name:     "job",
		Summary:  "Run scheduled commands with a timeout, a lock, a run record and alerts.",
		Details:  jobSchedule,
		Children: []*Command{jobRun(), jobList(), jobApply(), jobSched()},
	}
}

// jobSchedule says how a job is scheduled: by the OS (#109), or by job sched,
// which the OS supervises (#143).
const jobSchedule = `A job is any command aits job run runs once, with a timeout, a lock, a log
and alerts. Its schedule is cron's, or job sched's: one aits process that
runs the jobs of a file when they are due, under a LaunchAgent that job
apply --sched installs (see "aits job sched --help"). A crontab that backs
up notes nightly and checks prices every 5 minutes:

  MAILTO=""
  30 23 * * *  aits job run notes-backup --timeout 10m --on-alert ~/bin/notify -- ~/bin/notes-backup.sh
  */5 * * * *  aits job run prices --timeout 4m --on-alert ~/bin/notify -- ~/bin/check-prices

cron does not expand ~ in the path of a program, only the shell does: give
absolute paths in a launchd agent's ProgramArguments. A job does not get
your shell's environment (see "aits task --help"). To keep the jobs in a
file and install them on each machine, see "aits job apply --help".
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
"NAME  STATUS  FINISHED_AT  LAST_OK_AT  FAILING_SINCE" (empty columns as -),
then one line per jobs file a job sched has run,
"sched  FILE  CHECKED_AT"; with --json
{"jobs":[{name, last, last_ok_at, failing}], "sched":[{file, checked_at,
next, error}], "skipped":[NAME]}:
  last        The last run that was not skipped: job run's record.
  last_ok_at  When the job last ran ok.
  failing     While it fails: since, failures, signature, alerts,
              last_alert_at and unalerted, as job run --help describes.
  checked_at  The minute job sched last checked FILE: a stale time means
              no sched is running for it.
  next        [{name, at}]: when each of FILE's jobs for this host is due
              next, as of checked_at; without at, never.
  error       Why sched could not reread FILE; it still runs the jobs it
              read last.
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
				scheds, err := job.Heartbeats(env.Environ)
				if err != nil {
					return 0, taskError(err)
				}
				if env.JSON {
					writeJSON(env.Stdout, struct {
						Jobs    []job.State     `json:"jobs"`
						Sched   []job.Heartbeat `json:"sched"`
						Skipped []string        `json:"skipped"`
					}{states, scheds, skipped})
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
				for _, hb := range scheds {
					fmt.Fprintf(env.Stdout, "sched  %s  %s\n", printable(hb.File), stampOr(hb.CheckedAt))
					if hb.Error != "" {
						fmt.Fprintf(env.Stderr, "aits: job sched cannot reread %s: %s\n", printable(hb.File), printable(hb.Error))
					}
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

func jobApply() *Command {
	return &Command{
		Name:    "apply",
		Args:    "FILE",
		Summary: "Install the jobs FILE declares for this host into your crontab.",
		Details: `FILE is JSON, for example:

  {"path": "~/.local/bin:/opt/homebrew/bin:/usr/bin:/bin",
   "jobs": [{"name": "notes-backup", "schedule": "30 23 * * *",
             "timeout": "10m", "on_alert": "~/bin/notify", "env": ["NAME"],
             "hosts": ["mbp"], "command": ["~/bin/notes-backup.sh"]}]}

  name      Required: the job's NAME, as job run takes it.
  schedule  Required: 5 cron fields (minute hour day month weekday).
  command   Required: the program, an absolute path or ~/, and its
            arguments.
  timeout, env, on_alert
            job run's --timeout, --env (each) and --on-alert; on_alert is
            an absolute path or ~/.
  hosts     The short host names (the host name up to its first dot) to
            install the job on; without it, every host.
  path      The PATH every job starts with; each entry absolute or ~/.
~/ is $HOME. A key FILE does not know is an error.

Each job for this host becomes one line that runs this aits binary's job
run, with every argument quoted for sh. The lines go in a block of your
crontab between "# BEGIN aits job apply FILE" and "# END aits job apply
FILE" (FILE as an absolute path); the block is replaced as a whole, and
every line outside it is kept as it is. A job no longer in FILE, or no
longer for this host, is removed; with no job for this host the block is
removed. Edit FILE, not the block: apply refuses a block with a line it did
not write. A host's paths are checked only on that host. After installing,
apply reads the crontab back to check it.

--dry-run checks FILE and the crontab and reports what would change, and
changes nothing.

--sched (macOS): run FILE's jobs from job sched instead of cron, under the
claude.ai login and connectors a terminal has: cron cannot read the login
keychain, a LaunchAgent can. apply writes
~/Library/LaunchAgents/aits.sched.ID.plist (ID from FILE's path), which
runs this aits binary's "job sched FILE --json", at login and again when it
exits, with AITS_HOME when set and its output in the job directory's
sched-ID.log; loads it with launchctl bootstrap gui/UID, after a bootout
when it changed; then removes FILE's crontab block, so its jobs show as
removed. Edits to FILE need no apply: sched rereads it. apply without
--sched reverses this: it installs the block, then boots the agent out and
deletes its plist. apply refuses a plist at that path it did not write. A
LaunchAgent runs only while you are logged in; after upgrading aits,
restart it with launchctl kickstart -k gui/$(id -u)/LABEL.

Output: one line per job, "NAME  added|changed|removed|unchanged", then
"LABEL  added|changed|removed|unchanged" for the agent; with --json
{"host", "added", "changed", "removed", "unchanged", "sched"}: job names,
and, on macOS once --sched was used for FILE, sched {label, plist, change}.

Exit codes:
  0  Installed, nothing to change, or --dry-run.
  1  crontab or launchctl is not on PATH or failed; the host name could not
     be read.
  2  Usage error or invalid input: FILE, the crontab's block, a plist apply
     did not write, or --sched off macOS; nothing was changed.

Errors: unknown_flag, invalid_input (exit 2); internal (exit 1).
`,
		Setup: func(fs *flag.FlagSet) Runner {
			dryRun := fs.Bool("dry-run", false, "Report what would change, and change nothing.")
			sched := fs.Bool("sched", false, "Run the jobs from a LaunchAgent's job sched, not cron (macOS).")
			return func(ctx context.Context, env *Env, args []string) (int, error) {
				if len(args) != 1 {
					return 0, usageError("invalid_input", "expected FILE")
				}
				self, err := selfPath(env)
				if err != nil {
					return 0, failure("internal", "%v", err)
				}
				res, err := job.Apply(ctx, job.ApplyRequest{File: args[0], Aits: self, DryRun: *dryRun, Sched: *sched, Environ: env.Environ})
				if err != nil {
					return 0, taskError(err)
				}
				if env.JSON {
					writeJSON(env.Stdout, res)
					return exitOK, nil
				}
				for _, g := range []struct {
					what  string
					names []string
				}{{"added", res.Added}, {"changed", res.Changed}, {"removed", res.Removed}, {"unchanged", res.Unchanged}} {
					for _, name := range g.names {
						fmt.Fprintf(env.Stdout, "%s  %s\n", name, g.what)
					}
				}
				if res.Sched != nil {
					fmt.Fprintf(env.Stdout, "%s  %s\n", res.Sched.Label, res.Sched.Change)
				}
				return exitOK, nil
			}
		},
	}
}

func jobSched() *Command {
	return &Command{
		Name:    "sched",
		Args:    "FILE",
		Summary: "Run the jobs FILE declares for this host when they are due, until stopped.",
		Details: jobDirNote + `FILE is job apply's (see "aits job apply --help"). sched runs in the
foreground until a signal, and is meant to be started and restarted by a
supervisor: job apply --sched installs a LaunchAgent for it on macOS, where
a job then has the login keychain, and so claude's claude.ai login and
connectors, which cron does not.

Each minute sched rereads FILE and runs each job due since it last checked,
as job run does: its own process group, timeout, lock, log, run record and
alerts, with FILE's path as PATH and its env passed. A job is due when its
schedule matches a minute, in local time; cron's day rule holds (with both
day of month and day of week restricted, either matches). A job due more
than once since the last check, such as while the Mac slept or sched was
not running, runs once. A job whose previous run still holds its lock is
skipped. A FILE that cannot be reread is reported on stderr and in job
list; the jobs read last keep running. Only one sched runs per FILE.

On a signal (SIGTERM, SIGINT, SIGHUP) the running jobs are stopped as job
run stops them (status stopped), and sched exits 0 once they have.

Output: one line per run as it ends, job run's: "NAME  STATUS  ALERT", or
its record with --json. Each minute sched writes a heartbeat that job list
shows: when it last checked FILE and when each job is due next.

Exit codes:
  0  Stopped by a signal.
  1  Another sched holds FILE, or the job directory could not be written.
  2  Usage error or invalid input: FILE; nothing was run.

Errors: unknown_flag, invalid_input (exit 2); internal (exit 1).
`,
		Setup: func(*flag.FlagSet) Runner {
			return func(ctx context.Context, env *Env, args []string) (int, error) {
				if len(args) != 1 {
					return 0, usageError("invalid_input", "expected FILE")
				}
				err := job.Sched(ctx, job.SchedRequest{
					File: args[0], Environ: env.Environ, Kill: env.Kill,
					Report: func(rec job.Record) {
						if env.JSON {
							writeJSON(env.Stdout, rec)
							return
						}
						fmt.Fprintf(env.Stdout, "%s  %s  %s\n", rec.Name, rec.Status, rec.Alert)
						if rec.Message != "" {
							fmt.Fprintf(env.Stderr, "aits: job %s %s: %s\n", rec.Name, rec.Status, printable(rec.Message))
						}
					},
					Warn: func(msg string) { fmt.Fprintf(env.Stderr, "aits: job sched: %s\n", printable(msg)) },
				})
				if err != nil {
					return 0, taskError(err)
				}
				return exitOK, nil
			}
		},
	}
}

// selfPath is the absolute path aits was started as, found as the shell
// found it: a name without a slash on PATH. A symlink stays a symlink, so a
// crontab line follows an upgrade.
func selfPath(env *Env) (string, error) {
	switch {
	case env.Self == "":
		return "", fmt.Errorf("the path aits was started as is unknown")
	case strings.Contains(env.Self, "/"):
		return filepath.Abs(env.Self)
	}
	p, err := shell.LookPath(env.Environ, env.Self)
	if err != nil {
		return "", fmt.Errorf("%s is not on PATH", env.Self)
	}
	return p, nil
}
