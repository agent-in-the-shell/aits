package gw

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/agent-in-the-shell/aits/internal/cli"
)

// Root returns the aits-gw command tree. It shares the aits grammar (P4) and
// output rules (P5) through package cli.
func Root() *cli.Command {
	return cli.NewRoot("aits-gw", "Record events for agents and scripts (shadow mode: nothing is triggered or delivered).",
		&cli.Command{Name: "event", Summary: "Put events.", Children: []*cli.Command{eventPut()}},
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

SIGTERM, SIGINT or SIGHUP stops it after the event in hand.

Output: on stop, "stopped: N events ingested"; with --json
{"status":"stopped","ingested":N}, where N counts events read from the spool,
duplicates included.

Exit codes:
  0  Stopped by a signal.
  1  Another serve is running, or the spool or database failed.
  2  Usage error or invalid input.

Errors: unknown_flag, invalid_input (exit 2); already_serving, schema_too_new,
store (exit 1).
`,
		Setup: func(*flag.FlagSet) cli.Runner {
			return func(ctx context.Context, env *cli.Env, args []string) (int, error) {
				if len(args) != 0 {
					return 0, cli.UsageError("invalid_input", "serve takes no arguments")
				}
				p, err := resolve(env.Environ)
				if err != nil {
					return 0, dbError(err)
				}
				n, err := serve(ctx, p, env.Stderr)
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

// dbError maps a serve or status failure to its error code.
func dbError(err error) error {
	var tooNew errSchemaTooNew
	switch {
	case errors.Is(err, errServing):
		return cli.Failure("already_serving", "%v", err)
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
