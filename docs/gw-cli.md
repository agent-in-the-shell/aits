# aits-gw CLI reference

Generated from the help output of `aits-gw` by
`go test ./cmd/aits-gw -run TestCLIDocs -update`. Do not edit by hand.

## aits-gw

```text
Usage: aits-gw COMMAND

Record events for agents and scripts (shadow mode: nothing is triggered or delivered).

Commands:
  event   Put events.
  serve   Record spooled events in the database until stopped.
  status  Show whether serve is running and what is spooled and recorded.
  help    Show help for aits-gw or one of its commands.

Run 'aits-gw COMMAND --help' for details.

Errors: unknown_command, unknown_flag (exit 2).
```

## aits-gw event

```text
Usage: aits-gw event COMMAND

Put events.

Commands:
  put  Spool one event; aits-gw serve need not be running.

Run 'aits-gw event COMMAND --help' for details.

Errors: unknown_command, unknown_flag (exit 2).
```

## aits-gw event put

```text
Usage: aits-gw event put [flags] KIND [KEY=VALUE...]

Spool one event; aits-gw serve need not be running.

Flags:
  --json          Print the result, or the error, as JSON on stdout.
  --source-id ID  The event's ID at its source; required. Deduplicates re-sends.

KIND and each KEY hold only lowercase letters, digits, '.', '_' and '-',
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
```

## aits-gw serve

```text
Usage: aits-gw serve [flags]

Record spooled events in the database until stopped.

Flags:
  --inbox DIR  Also copy the files under DIR, read-only, into inbox.file events.
  --json       Print the result, or the error, as JSON on stdout.

serve runs in the foreground and never detaches; run it under a supervisor
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
```

## aits-gw status

```text
Usage: aits-gw status [flags]

Show whether serve is running and what is spooled and recorded.

Flags:
  --json  Print the result, or the error, as JSON on stdout.

status reads the spool and the database directly; it works whether or not
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
```

## aits-gw help

```text
Usage: aits-gw help [flags] [COMMAND...]

Show help for aits-gw or one of its commands.

Flags:
  --json  Print the result, or the error, as JSON on stdout.

Errors: unknown_command, unknown_flag (exit 2).
```
