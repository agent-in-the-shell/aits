package cli

import (
	"context"
	"flag"
	"fmt"
	"runtime/debug"
	"strings"
)

// versionCommand is "aits version": the main module version Go recorded in
// the binary when it was built (#102).
func versionCommand() *Command {
	return &Command{
		Name:    "version",
		Summary: "Print the version of this aits binary.",
		Details: `Output:
  The main module version Go recorded when the binary was built: a release
  tag, or a pseudo-version ending in the commit, e.g.
  v0.0.0-20261015142825-3556be1bc9f6, with +dirty for uncommitted changes.
  With --json: {"version":"…"}. "go build" in a git checkout and
  "go install …@VERSION" record it; -buildvcs=false or a copy without .git
  does not.

Exit codes:
  0  Printed.
  1  The binary records no version.
  2  Usage error or invalid input.

Errors: unknown_flag, invalid_input (exit 2); unknown_version (exit 1).
`,
		Setup: func(*flag.FlagSet) Runner {
			return func(_ context.Context, env *Env, args []string) (int, error) {
				if len(args) != 0 {
					return 0, usageError("invalid_input", "version takes no arguments")
				}
				info, ok := debug.ReadBuildInfo()
				// Without version control information Go records "(devel)".
				if !ok || !strings.HasPrefix(info.Main.Version, "v") {
					return 0, failure("unknown_version", "this aits records no version (built with -buildvcs=false or outside a git checkout)")
				}
				if env.JSON {
					writeJSON(env.Stdout, map[string]string{"version": info.Main.Version})
				} else {
					fmt.Fprintln(env.Stdout, info.Main.Version)
				}
				return exitOK, nil
			}
		},
	}
}
