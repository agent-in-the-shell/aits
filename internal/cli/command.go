package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Runner runs a leaf command with its positional arguments. It returns the
// exit code, or an error that Run reports on the error channel (P5).
type Runner func(ctx context.Context, env *Env, args []string) (int, error)

// Command is a node of the command tree: a group (Children) or a leaf (Setup).
type Command struct {
	Name     string
	Args     string // positional synopsis for help, e.g. "(PROMPT | -)"
	Summary  string
	Details  string // printed verbatim after a leaf's flags in help
	Children []*Command
	// Setup declares the leaf's flags on fs and returns its Runner. It must do
	// nothing else: help calls it to list the flags.
	Setup func(fs *flag.FlagSet) Runner
}

// Root returns the aits command tree.
func Root() *Command {
	root := &Command{
		Name:     "aits",
		Summary:  "Run coding agents from the command line.",
		Children: []*Command{shellGroup(), sessionGroup(), taskGroup()},
	}
	root.Children = append(root.Children, helpCommand(root))
	return root
}

func (c *Command) child(name string) *Command {
	for _, ch := range c.Children {
		if ch.Name == name {
			return ch
		}
	}
	return nil
}

// flags returns the flag set c parses: --json, and for a leaf its own flags
// and its Runner. A group takes no other flag (isGlobalFlag).
func (c *Command) flags(path []string) (*flag.FlagSet, Runner) {
	fs := newFlagSet(strings.Join(path, " "))
	fs.Bool("json", false, "Print the result, or the error, as JSON on stdout.")
	if c.Setup == nil {
		return fs, nil
	}
	return fs, c.Setup(fs)
}

// isGlobalFlag reports whether a may appear before the command path reaches a
// leaf: only --json and --help may.
func isGlobalFlag(a string) bool {
	name, _, _, ok := flagName(a)
	return ok && (name == "json" || isHelp(name))
}

// resolve walks args down the tree. Command names come first; before a leaf
// is reached only global flags are accepted, because a leaf's flags are
// unknown until the leaf is (spec §4). It returns the command, its path and
// the arguments left for it.
func resolve(root *Command, args []string) (*Command, []string, []string, error) {
	cmd, path := root, []string{root.Name}
	var rest []string
	for i, a := range args {
		if cmd.Setup != nil {
			return cmd, path, append(rest, args[i:]...), nil
		}
		switch {
		case a == "--":
			if i+1 < len(args) {
				return nil, nil, nil, usageError("unknown_command", "unknown command %q for %s", args[i+1], strings.Join(path, " "))
			}
		case isFlag(a):
			if !isGlobalFlag(a) {
				return nil, nil, nil, usageError("unknown_flag", "unknown flag %s for %s", a, strings.Join(path, " "))
			}
			rest = append(rest, a)
		default:
			next := cmd.child(a)
			if next == nil {
				return nil, nil, nil, usageError("unknown_command", "unknown command %q for %s", a, strings.Join(path, " "))
			}
			cmd, path = next, append(path, a)
		}
	}
	return cmd, path, rest, nil
}

func dispatch(ctx context.Context, root *Command, args []string, env *Env) (int, error) {
	cmd, path, rest, err := resolve(root, args)
	if err != nil {
		return 0, err
	}
	fs, run := cmd.flags(path)
	// fs's --json is ignored: env.JSON comes from the whole line (wantsJSON).
	positional, err := parseArgs(fs, rest)
	if err != nil && err != errHelp {
		return 0, err
	}
	if err == errHelp || run == nil {
		writeHelp(env.Stdout, path, cmd)
		return exitOK, nil
	}
	return run(ctx, env, positional)
}

// writeHelp prints the help for cmd, reached through path.
func writeHelp(w io.Writer, path []string, cmd *Command) {
	name := strings.Join(path, " ")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if cmd.Setup == nil {
		fmt.Fprintf(tw, "Usage: %s COMMAND\n\n%s\n\nCommands:\n", name, cmd.Summary)
		for _, ch := range cmd.Children {
			fmt.Fprintf(tw, "  %s\t%s\n", ch.Name, ch.Summary)
		}
		fmt.Fprintf(tw, "\nRun '%s COMMAND --help' for details.\n", name)
		fmt.Fprintln(tw, "\nErrors: unknown_command, unknown_flag (exit 2).")
	} else {
		usage := "Usage: " + name + " [flags]"
		if cmd.Args != "" {
			usage += " " + cmd.Args
		}
		fmt.Fprintf(tw, "%s\n\n%s\n\nFlags:\n", usage, cmd.Summary)
		fs, _ := cmd.flags(path)
		fs.VisitAll(func(f *flag.Flag) {
			typ, text := flag.UnquoteUsage(f)
			left := "  --" + f.Name
			if typ != "" {
				left += " " + typ
			}
			fmt.Fprintf(tw, "%s\t%s%s\n", left, text, defaultNote(f))
		})
	}
	_ = tw.Flush()
	if cmd.Details != "" {
		fmt.Fprintf(w, "\n%s", cmd.Details)
	}
}

func defaultNote(f *flag.Flag) string {
	switch f.DefValue {
	case "", "false", "0":
		return ""
	}
	return " (default " + f.DefValue + ")"
}

// helpCommand is "aits help [COMMAND...]".
func helpCommand(root *Command) *Command {
	return &Command{
		Name:    "help",
		Args:    "[COMMAND...]",
		Summary: "Show help for aits or one of its commands.",
		Details: "Errors: unknown_command, unknown_flag (exit 2).\n",
		Setup: func(*flag.FlagSet) Runner {
			return func(_ context.Context, env *Env, args []string) (int, error) {
				cmd, path, rest, err := resolve(root, args)
				if err != nil {
					return 0, err
				}
				if len(rest) > 0 {
					return 0, usageError("unknown_command", "unknown command %q for %s", rest[0], strings.Join(path, " "))
				}
				writeHelp(env.Stdout, path, cmd)
				return exitOK, nil
			}
		},
	}
}
