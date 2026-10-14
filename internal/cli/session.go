package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/agent-in-the-shell/aits/internal/session"
)

func sessionGroup() *Command {
	return &Command{
		Name:     "session",
		Summary:  "Search the coding agents' own conversation transcripts.",
		Children: []*Command{searchCommand()},
	}
}

// searchDetails documents the output of "aits session search" (P5).
const searchDetails = `Output:
  With --json, stdout is one JSON object:
  {"results":[...],"skipped":{"files":N,"lines":N}}. Without --json, each
  result is a line "UPDATED_AT  AGENT  SESSION_ID  CWD" and an indented
  snippet, and anything skipped is reported on stderr. No match is not an
  error.

Result fields:
  agent        The agent whose transcript matched.
  session_id   Pass it to "aits shell submit --session" to continue.
  cwd          The session's working directory, symlinks resolved.
  updated_at   When the transcript file last changed, in UTC; newest first.
  snippet      Text around the first term in the newest message with it.

Skipped:
  files        Transcripts that could not be read or name no session or cwd.
  lines        Lines that were not JSON or were longer than 64 MiB.

Searched:
  User and assistant message text, and prompts queued while the agent was
  working; every term somewhere in the session, ignoring case. Not tool calls
  or their output, model thinking, subagent transcripts (Claude's or Codex's),
  compaction summaries, or text the agent CLI adds to a turn by itself
  (instructions such as AGENTS.md, reminders, notifications, "!" shell
  output, interruption and API-error notices).

Transcripts:
  claude  $CLAUDE_CONFIG_DIR/projects/*/*.jsonl (default ~/.claude)
  codex   $CODEX_HOME/sessions/*/*/*/rollout-*.jsonl (default ~/.codex)
  Subdirectories that cannot be read are passed over without a count; if a
  vendor changes this layout, its sessions are not found.

Exit codes:
  0  The search ran, with or without results.
  1  Stopped by a signal, or an unexpected failure.
  2  Usage error or invalid input.
`

func searchCommand() *Command {
	return &Command{
		Name:    "search",
		Args:    "TERM...",
		Summary: "Find conversations that contain every term, with an id to continue them.",
		Details: searchDetails,
		Setup: func(fs *flag.FlagSet) Runner {
			agent := fs.String("agent", "", "Search only `AGENT`'s transcripts. One of: "+strings.Join(session.Agents(), ", ")+".")
			cwd := fs.String("cwd", "", "Only sessions whose working directory is `DIR` or below it.")
			var since time.Duration
			fs.Func("since", "Only transcripts changed within `DURATION`, e.g. 72h.", func(v string) error {
				d, err := time.ParseDuration(v)
				if err != nil {
					return err
				}
				if d <= 0 {
					return errors.New("must be positive")
				}
				since = d
				return nil
			})
			limit := fs.Int("limit", session.DefaultLimit, "Return at most `N` results.")
			return func(ctx context.Context, env *Env, args []string) (int, error) {
				var terms []string
				for _, a := range args {
					terms = append(terms, strings.Fields(a)...)
				}
				dir := *cwd
				if dir != "" {
					abs, err := filepath.Abs(dir)
					if err != nil {
						return 0, failure("internal", "resolve --cwd: %v", err)
					}
					// Agents record the directory with symlinks resolved (on
					// macOS /tmp is /private/tmp); keep the path as given when
					// it no longer exists.
					if real, err := filepath.EvalSymlinks(abs); err == nil {
						abs = real
					}
					dir = abs
				}
				out, err := session.Search(ctx, session.Query{
					Terms: terms, Agent: *agent, Cwd: dir, Since: since, Limit: *limit, Environ: env.Environ,
				})
				if err != nil {
					return 0, searchError(err)
				}
				writeSearch(env, out)
				return exitOK, nil
			}
		},
	}
}

func searchError(err error) error {
	var invalid *session.InvalidQueryError
	switch {
	case errors.As(err, &invalid):
		return usageError("invalid_input", "%s", invalid.Reason)
	case errors.Is(err, context.Canceled):
		return failure("stopped", "stopped by a signal")
	}
	return failure("internal", "%v", err)
}

func writeSearch(env *Env, out session.Output) {
	if env.JSON {
		writeJSON(env.Stdout, out)
		return
	}
	for _, r := range out.Results {
		fmt.Fprintf(env.Stdout, "%s  %s  %s  %s\n    %s\n", r.UpdatedAt.Format(time.RFC3339), r.Agent, r.SessionID, r.Cwd, r.Snippet)
	}
	if s := out.Skipped; s.Files+s.Lines > 0 {
		fmt.Fprintf(env.Stderr, "aits: skipped %d files and %d lines that could not be read\n", s.Files, s.Lines)
	}
}
