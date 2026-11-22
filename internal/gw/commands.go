package gw

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/agent-in-the-shell/aits/internal/cli"
)

// Root returns the aits-gw command tree. It shares the aits grammar (P4) and
// output rules (P5) through package cli.
func Root() *cli.Command {
	return cli.NewRoot("aits-gw", "Record events for agents and scripts (shadow mode: nothing is triggered or delivered).",
		&cli.Command{Name: "event", Summary: "Put events.", Children: []*cli.Command{eventPut()}},
		&cli.Command{Name: "inbox", Summary: "Compare a file inbox with its events.", Children: []*cli.Command{inboxCheck()}},
		serveCommand(), statusCommand())
}

// storeError reports a failure to reach the gateway's files.
func storeError(err error) error { return cli.Failure("store", "%v", err) }

func eventPut() *cli.Command {
	return &cli.Command{
		Name:    "put",
		Args:    "KIND [KEY=VALUE...]",
		Summary: "Spool one event; aits-gw serve need not be running.",
		Details: `KIND and each KEY hold only lowercase letters, digits, '.', '_' and '-',
starting with a letter or digit, up to 128 bytes. --source-id names the
event at its source: serve records each (KIND, --source-id) once and counts
a later put of the same pair as a duplicate. VALUEs are strings; a KEY is
given once. The event, encoded, is at most 64 KiB.

The event is written to $AITS_HOME/gw/spool (default
~/.local/share/aits/gw/spool) through a temporary file and a rename, so
put succeeds whether or not serve is running.

Output: the event id; with --json
{"status":"spooled","id":"…","kind":"…","source_id":"…"}.

Exit codes:
  0  Spooled.
  1  The spool could not be written.
  2  Usage error or invalid input.

Errors: unknown_flag, invalid_input (exit 2); store (exit 1).
`,
		Setup: func(fs *flag.FlagSet) cli.Runner {
			source := fs.String("source-id", "", "The event's `ID` at its source; required. Deduplicates re-sends.")
			return func(_ context.Context, env *cli.Env, args []string) (int, error) {
				if len(args) == 0 {
					return 0, cli.UsageError("invalid_input", "event put needs a KIND")
				}
				if *source == "" {
					return 0, cli.UsageError("invalid_input", "event put needs --source-id")
				}
				now := time.Now()
				e := &Event{ID: newID(now), Kind: args[0], SourceID: *source, Payload: map[string]string{}, PutAt: now.UTC().Format(time.RFC3339Nano)}
				for _, kv := range args[1:] {
					k, v, ok := strings.Cut(kv, "=")
					if !ok {
						return 0, cli.UsageError("invalid_input", "argument %q is not KEY=VALUE", kv)
					}
					if _, dup := e.Payload[k]; dup {
						return 0, cli.UsageError("invalid_input", "payload key %q is given more than once", k)
					}
					e.Payload[k] = v
				}
				if err := e.check(); err != nil {
					return 0, cli.UsageError("invalid_input", "%v", err)
				}
				p, err := resolve(env.Environ)
				if err != nil {
					return 0, storeError(err)
				}
				if err := put(p, e); errors.Is(err, errTooLarge) {
					return 0, cli.UsageError("invalid_input", "%v", err)
				} else if err != nil {
					return 0, storeError(err)
				}
				if env.JSON {
					cli.WriteJSON(env.Stdout, map[string]string{"status": "spooled", "id": e.ID, "kind": e.Kind, "source_id": e.SourceID})
				} else {
					fmt.Fprintln(env.Stdout, e.ID)
				}
				return 0, nil
			}
		},
	}
}

func serveCommand() *cli.Command {
	return &cli.Command{
		Name:    "serve",
		Summary: "Record spooled events in the database until stopped.",
		Details: `serve runs in the foreground and never detaches; run it under a supervisor
such as launchd or systemd. It reads the spool every 0.5s, records each event
in $AITS_HOME/gw/gw.db (SQLite) and removes its spool file. An event whose
(KIND, --source-id) is already recorded is counted as a duplicate, not
recorded again. A spool file that is not an event is moved to
spool/rejected and reported on stderr; serve keeps going. Only one serve
runs at a time.

With --inbox DIR, each poll also walks DIR and its subdirectories, which
another program owns, and copies each regular file whose name does not start
with "." into an event, once its mtime is 2s old:
  kind       inbox.file
  source_id  the file's name, so a file the other program moves into a
             subdirectory (such as done/) stays one event
  payload    name; dir, the subdirectory it was first seen in ("." for DIR);
             size; content, the bytes if valid UTF-8 and at most 32 KiB,
             else content_omitted: too_large or not_utf8
  put_at     the file's mtime
serve only reads DIR: it never creates, moves, rewrites, removes or changes
the mode of anything there. Each name is copied once; a restart does not copy
recorded names again. A file that cannot be copied (a symlink, an unreadable
file) is reported once on stderr and skipped.

SIGTERM, SIGINT or SIGHUP stops it after the event in hand.

Output: on stop, "stopped: N events ingested"; with --json
{"status":"stopped","ingested":N}, where N counts events read from the spool,
duplicates included, and files copied from the inbox.

Exit codes:
  0  Stopped by a signal.
  1  Another serve is running, the inbox could not be walked, or the spool
     or database failed.
  2  Usage error or invalid input.

Errors: unknown_flag, invalid_input (exit 2); already_serving, inbox,
schema_too_new, store (exit 1).
`,
		Setup: func(fs *flag.FlagSet) cli.Runner {
			inboxDir := cli.NonEmpty(fs, "inbox", "Also copy the files under `DIR`, read-only, into inbox.file events.", "a directory")
			return func(ctx context.Context, env *cli.Env, args []string) (int, error) {
				if len(args) != 0 {
					return 0, cli.UsageError("invalid_input", "serve takes no arguments")
				}
				if *inboxDir != "" {
					if err := checkInboxDir(*inboxDir); err != nil {
						return 0, err
					}
				}
				p, err := resolve(env.Environ)
				if err != nil {
					return 0, dbError(err)
				}
				n, err := serve(ctx, p, *inboxDir, env.Stderr)
				if err != nil {
					return 0, dbError(err)
				}
				if env.JSON {
					cli.WriteJSON(env.Stdout, map[string]any{"status": "stopped", "ingested": n})
				} else {
					fmt.Fprintf(env.Stdout, "stopped: %d events ingested\n", n)
				}
				return 0, nil
			}
		},
	}
}

// checkInboxDir refuses an --inbox that is not a directory.
func checkInboxDir(dir string) error {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return cli.UsageError("invalid_input", "--inbox %s is not a directory", dir)
	}
	return nil
}

func inboxCheck() *cli.Command {
	return &cli.Command{
		Name:    "check",
		Summary: "Compare the files under an inbox with the events serve --inbox recorded.",
		Details: `check walks DIR as serve --inbox does (regular files whose name does not
start with ".", outside dot-named directories) and matches each file name with
the inbox.file event of that source id in $AITS_HOME/gw/gw.db. It only reads
DIR, works whether or not serve is running, and creates an empty database if
there is none.

Output (--json):
  {"status":"ok|mismatch","files":N,"events":N,"matched":N,"pending":N,
   "skipped":N,"missed":[NAME...],"extra":[{"name":…,"reason":…}...],
   "duplicates":[{"name":…,"reason":…}...]}
  files       files under DIR that serve --inbox would copy
  events      inbox.file events recorded
  matched     names with one file and one event whose size, and content
              when copied, equal the file's
  pending     unrecorded files less than 10s old; not counted as missed
  skipped     entries serve --inbox never copies (not a regular file, or a
              name it refuses)
  missed      names of files older than that with no event
  extra       events with no file (reason no_file), or whose file's size or
              content differs (changed)
  duplicates  an event put more than once (reason seen), or one name on more
              than one file under DIR (files)
status is ok when missed, extra and duplicates are empty, else mismatch;
both exit 0. Lists are sorted by name. Without --json, one "name: value"
line per count, then each list entry indented under its name.

Exit codes:
  0  Compared (status ok or mismatch).
  1  DIR or the database could not be read.
  2  Usage error or invalid input.

Errors: unknown_flag, invalid_input (exit 2); inbox, schema_too_new, store
(exit 1).
`,
		Setup: func(fs *flag.FlagSet) cli.Runner {
			inboxDir := cli.NonEmpty(fs, "inbox", "The inbox `DIR` that serve --inbox copies; required.", "a directory")
			return func(ctx context.Context, env *cli.Env, args []string) (int, error) {
				if len(args) != 0 {
					return 0, cli.UsageError("invalid_input", "check takes no arguments")
				}
				if *inboxDir == "" {
					return 0, cli.UsageError("invalid_input", "check needs --inbox")
				}
				if err := checkInboxDir(*inboxDir); err != nil {
					return 0, err
				}
				r, err := check(ctx, env.Environ, *inboxDir, time.Now())
				if err != nil {
					return 0, dbError(err)
				}
				if env.JSON {
					cli.WriteJSON(env.Stdout, r)
					return 0, nil
				}
				fmt.Fprintf(env.Stdout, "status: %s\nfiles: %d\nevents: %d\nmatched: %d\npending: %d\nskipped: %d\nmissed: %d\n",
					r.Status, r.Files, r.Events, r.Matched, r.Pending, r.Skipped, len(r.Missed))
				for _, n := range r.Missed {
					fmt.Fprintf(env.Stdout, "  %s\n", n)
				}
				fmt.Fprintf(env.Stdout, "extra: %d\n", len(r.Extra))
				for _, e := range r.Extra {
					fmt.Fprintf(env.Stdout, "  %s  %s\n", e.Name, e.Reason)
				}
				fmt.Fprintf(env.Stdout, "duplicates: %d\n", len(r.Duplicates))
				for _, e := range r.Duplicates {
					fmt.Fprintf(env.Stdout, "  %s  %s\n", e.Name, e.Reason)
				}
				return 0, nil
			}
		},
	}
}

// dbError maps a serve, status or check failure to its error code.
func dbError(err error) error {
	var tooNew errSchemaTooNew
	var walk errInbox
	switch {
	case errors.Is(err, errServing):
		return cli.Failure("already_serving", "%v", err)
	case errors.As(err, &walk):
		return cli.Failure("inbox", "%v", err)
	case errors.As(err, &tooNew):
		return cli.Failure("schema_too_new", "%v", err)
	}
	return storeError(err)
}

func statusCommand() *cli.Command {
	return &cli.Command{
		Name:    "status",
		Summary: "Show whether serve is running and what is spooled and recorded.",
		Details: `status reads the spool and the database directly; it works whether or not
serve is running, and creates an empty database if there is none.

Output (--json):
  {"status":"ok","serving":BOOL,"spooled":N,"rejected":N,"events":N,
   "duplicates":N,"last_ingested_at":"RFC 3339 time or empty",
   "schema_version":N}
  serving     a serve holds the lock now
  spooled     events put and not yet recorded
  rejected    spool files serve could not read as an event
  events      events recorded, one per (KIND, --source-id)
  duplicates  later puts of a recorded (KIND, --source-id)
Without --json, one "name: value" line per field.

Exit codes:
  0  Printed.
  1  The spool or database could not be read.
  2  Usage error or invalid input.

Errors: unknown_flag, invalid_input (exit 2); schema_too_new, store (exit 1).
`,
		Setup: func(*flag.FlagSet) cli.Runner {
			return func(ctx context.Context, env *cli.Env, args []string) (int, error) {
				if len(args) != 0 {
					return 0, cli.UsageError("invalid_input", "status takes no arguments")
				}
				st, err := readStatus(ctx, env.Environ)
				if err != nil {
					return 0, dbError(err)
				}
				if env.JSON {
					cli.WriteJSON(env.Stdout, st)
				} else {
					fmt.Fprintf(env.Stdout, "serving: %t\nspooled: %d\nrejected: %d\nevents: %d\nduplicates: %d\nlast_ingested_at: %s\nschema_version: %d\n",
						st.Serving, st.Spooled, st.Rejected, st.Events, st.Duplicates, st.LastIngestedAt, st.SchemaVersion)
				}
				return 0, nil
			}
		},
	}
}

// statusResult is what status prints.
type statusResult struct {
	Status         string `json:"status"`
	Serving        bool   `json:"serving"`
	Spooled        int    `json:"spooled"`
	Rejected       int    `json:"rejected"`
	Events         int    `json:"events"`
	Duplicates     int    `json:"duplicates"`
	LastIngestedAt string `json:"last_ingested_at"`
	SchemaVersion  int    `json:"schema_version"`
}

// readStatus reads the lock, the spool and the database.
func readStatus(ctx context.Context, environ []string) (statusResult, error) {
	st := statusResult{Status: "ok"}
	p, err := resolve(environ)
	if err != nil {
		return st, err
	}
	if st.Serving, err = serving(p); err != nil {
		return st, err
	}
	spool, err := spooled(p.spool)
	if err != nil {
		return st, err
	}
	rejected, err := spooled(p.rejected)
	if err != nil {
		return st, err
	}
	st.Spooled, st.Rejected = len(spool), len(rejected)
	db, err := openDB(ctx, p.dir)
	if err != nil {
		return st, err
	}
	defer db.Close()
	st.SchemaVersion = len(migrations) // openDB refuses any other version
	err = db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(seen - 1), 0), COALESCE(MAX(ingested_at), '') FROM events`).
		Scan(&st.Events, &st.Duplicates, &st.LastIngestedAt)
	return st, err
}
