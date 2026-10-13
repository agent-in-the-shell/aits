package cli

import (
	"errors"
	"flag"
	"io"
	"strings"
)

var errHelp = errors.New("help requested")

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

// isFlag reports whether a looks like a flag. A lone "-" is a positional
// argument: it means "read standard input".
func isFlag(a string) bool { return len(a) > 1 && a[0] == '-' }

// flagName splits a flag token such as "--name=value" into its parts.
func flagName(a string) (name, value string, hasValue bool) {
	return strings.Cut(strings.TrimLeft(a, "-"), "=")
}

// parseArgs parses args against fs, allowing flags before, between and after
// positional arguments; "--" ends flag parsing (P4). It returns the
// positional arguments in order.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !isFlag(a) {
			positional = append(positional, a)
			continue
		}
		name, _, hasValue := flagName(a)
		if name == "h" || name == "help" {
			return nil, errHelp
		}
		f := fs.Lookup(name)
		if f == nil {
			return nil, usageError("unknown_flag", "unknown flag %s", a)
		}
		flags = append(flags, a)
		if hasValue || isBoolFlag(f) {
			continue
		}
		if i+1 == len(args) {
			return nil, usageError("invalid_input", "flag %s needs a value", a)
		}
		i++
		flags = append(flags, args[i])
	}
	if err := fs.Parse(flags); err != nil {
		return nil, usageError("invalid_input", "%v", err)
	}
	return positional, nil
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}
