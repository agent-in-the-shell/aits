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

// flagName splits a flag token such as "--name=value" into its parts; ok is
// false when a is not a flag.
func flagName(a string) (name, value string, hasValue, ok bool) {
	if !isFlag(a) {
		return "", "", false, false
	}
	name, value, hasValue = strings.Cut(strings.TrimLeft(a, "-"), "=")
	return name, value, hasValue, true
}

func isHelp(name string) bool { return name == "h" || name == "help" }

// parseArgs parses args against fs, allowing flags before, between and after
// positional arguments; "--" ends flag parsing (P4). A flag that takes one
// value may be given once (P6: the last one does not silently win). --help
// is honoured only once the whole line parses. It returns the positional
// arguments in order.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, positional []string
	seen := map[string]bool{}
	help := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		name, _, hasValue, ok := flagName(a)
		if !ok {
			positional = append(positional, a)
			continue
		}
		if isHelp(name) {
			help = true
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			return nil, usageError("unknown_flag", "unknown flag %s", a)
		}
		_, many := f.Value.(*list)
		if seen[f.Name] && !many && !isBoolFlag(f) {
			return nil, usageError("invalid_input", "flag --%s is given more than once", f.Name)
		}
		seen[f.Name] = true
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
	if help {
		return nil, errHelp
	}
	return positional, nil
}

// list is the value of a flag that may be given more than once.
type list []string

func (l *list) String() string     { return "" }
func (l *list) Set(s string) error { *l = append(*l, s); return nil }

// repeated declares a flag that may be given more than once, and says so in
// its help; each value is appended to the returned list.
func repeated(fs *flag.FlagSet, name, usage string) *[]string {
	l := new(list)
	fs.Var(l, name, usage+" Repeatable.")
	return (*[]string)(l)
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// nonEmpty declares a string flag that refuses an explicitly empty value, so
// "--name=" is never read as "not given" (P6). The error reads
// `invalid value "" for flag -name: needs <need>`.
func nonEmpty(fs *flag.FlagSet, name, usage, need string) *string {
	v := new(string)
	fs.Func(name, usage, func(s string) error {
		if s == "" {
			return errors.New("needs " + need)
		}
		*v = s
		return nil
	})
	return v
}
