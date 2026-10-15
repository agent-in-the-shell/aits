package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/agent-in-the-shell/aits/internal/cli"
	"github.com/agent-in-the-shell/aits/internal/shell"
)

var update = flag.Bool("update", false, "rewrite docs/cli.md from the binary's help output")

// TestCLIDocs enforces P13: docs/cli.md is exactly the help the binary prints.
func TestCLIDocs(t *testing.T) {
	var b strings.Builder
	b.WriteString("# CLI reference\n\nGenerated from the help output of `aits` by\n" +
		"`go test ./cmd/aits -run TestCLIDocs -update`. Do not edit by hand.\n")
	walk(cli.Root(), nil, func(path []string, _ *cli.Command) {
		out := call{args: append(slices.Clone(path), "--help")}.run(t)
		if out.code != 0 {
			t.Fatalf("aits %s --help: exit %d", strings.Join(path, " "), out.code)
		}
		fmt.Fprintf(&b, "\n## %s\n\n```text\n%s```\n", strings.Join(append([]string{"aits"}, path...), " "), out.stdout)
	})
	const file = "../../docs/cli.md"
	if *update {
		if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v; run: go test ./cmd/aits -run TestCLIDocs -update", err)
	}
	if string(want) != b.String() {
		t.Fatalf("docs/cli.md is out of date; run: go test ./cmd/aits -run TestCLIDocs -update")
	}
}

// TestSubmitHelpDocumentsOutput enforces the P5 rule that output shapes are
// documented in help: every result field, status and error code submit can
// produce has a row in "aits shell submit --help", and each error code's row
// names the exit code it causes.
func TestSubmitHelpDocumentsOutput(t *testing.T) {
	// rows maps "Section name" to the help line documenting name, where
	// Section is the first word of the heading above it, e.g. "Statuses".
	rows := map[string]string{}
	var section string
	for _, line := range strings.Split(call{args: []string{"shell", "submit", "--help"}}.run(t).stdout, "\n") {
		f := strings.Fields(strings.ReplaceAll(line, ":", " "))
		switch {
		case len(f) == 0:
		case !strings.HasPrefix(line, " "):
			section = f[0]
		default:
			rows[section+" "+f[0]] = line
		}
	}
	rt := reflect.TypeFor[shell.Result]()
	for i := range rt.NumField() {
		if name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ","); rows["Result "+name] == "" {
			t.Errorf("result field %q is not documented in help", name)
		}
	}
	for _, m := range sourceMatches(t, "../../internal/shell/*.go", `\bStatus\s*=\s*"(\w+)"`) {
		if rows["Statuses "+m[1]] == "" {
			t.Errorf("status %q is not documented in help", m[1])
		}
	}
	documented := helpErrors(t, []string{"shell", "submit"})
	// The files on submit's path: its command, flag parsing, and the error
	// report. Group-level codes (command.go) are not submit's (#72).
	for code, exit := range errorCodes(t, "../../internal/cli/shell.go", "../../internal/cli/parse.go", "../../internal/cli/cli.go") {
		if documented[code] != exit {
			t.Errorf("error code %q is not documented in help with %s", code, exit)
		}
	}
}

// TestSearchHelpDocumentsErrors enforces P5 for "aits session search": every
// error code internal/cli/session.go can produce is on the help's Errors line
// with the exit code it causes (#62).
func TestSearchHelpDocumentsErrors(t *testing.T) {
	documented := helpErrors(t, []string{"session", "search"})
	for code, exit := range errorCodes(t, "../../internal/cli/session.go") {
		if documented[code] != exit {
			t.Errorf("error code %q is not in the help's Errors with %s: %v", code, exit, documented)
		}
	}
}

// TestTaskHelpNamesParentFaults enforces P5 for follow-ups: the reasons
// internal/task fails a follow-up whose parent cannot be resumed are all
// named, in source order, in the help of show, list and create (#79).
func TestTaskHelpNamesParentFaults(t *testing.T) {
	var faults []string
	for _, m := range sourceMatches(t, "../../internal/task/task.go", `case p\b[^:]*:\s*return "([^"]+)"`) {
		faults = append(faults, m[1])
	}
	want := "parent " + strings.Join(faults[:len(faults)-1], ", ") + " or " + faults[len(faults)-1]
	for _, cmd := range []string{"show", "list", "create"} {
		if help := helpLine(t, "task", cmd); !strings.Contains(help, want) {
			t.Errorf("task %s --help does not say %q", cmd, want)
		}
	}
}

// helpLine returns the help of the command at path as one line of words, so
// that a check does not depend on where the help wraps.
func helpLine(t *testing.T, path ...string) string {
	t.Helper()
	return strings.Join(strings.Fields(call{args: append(path, "--help")}.run(t).stdout), " ")
}

// helpErrors maps each error code that the help of the command at path lists
// to its exit code, e.g. "exit 2". It reads both forms of the Errors section:
// one line, "Errors: a, b (exit 2); c (exit 1).", or a heading followed by
// rows "  CODE  exit N  WHY".
func helpErrors(t *testing.T, path []string) map[string]string {
	t.Helper()
	lines := strings.Split(call{args: append(slices.Clone(path), "--help")}.run(t).stdout, "\n")
	errs := map[string]string{}
	for i, line := range lines {
		if after, ok := strings.CutPrefix(line, "Errors: "); ok {
			for _, group := range strings.Split(strings.TrimSuffix(after, "."), "; ") {
				codes, exit, _ := strings.Cut(strings.TrimSuffix(group, ")"), " (")
				for _, c := range strings.Split(codes, ", ") {
					errs[c] = exit
				}
			}
		} else if strings.HasPrefix(line, "Errors") {
			for _, row := range lines[i+1:] {
				f := strings.Fields(row)
				if !strings.HasPrefix(row, " ") || len(f) < 3 {
					break
				}
				errs[f[0]] = f[1] + " " + f[2]
			}
		}
	}
	return errs
}

// commandPath returns the leading args that name a command, e.g. "task
// create" of "task create --agent claude hi".
func commandPath(args []string) []string {
	cmd, n := cli.Root(), 0
	for ; n < len(args); n++ {
		i := slices.IndexFunc(cmd.Children, func(c *cli.Command) bool { return c.Name == args[n] })
		if i < 0 {
			break
		}
		cmd = cmd.Children[i]
	}
	return args[:n]
}

// errorCodes maps each error code that the files the globs name pass to
// usageError or failure to the exit code it causes.
func errorCodes(t *testing.T, globs ...string) map[string]string {
	t.Helper()
	exit := map[string]string{"usageError": "exit 2", "failure": "exit 1"}
	codes := map[string]string{}
	for _, glob := range globs {
		for _, m := range sourceMatches(t, glob, `\b(usageError|failure)\(\s*"(\w+)"`) {
			codes[m[2]] = exit[m[1]]
		}
	}
	return codes
}

// sourceMatches returns the submatches of pattern in the non-test Go files
// that glob names, failing the test if there are none.
func sourceMatches(t *testing.T, glob, pattern string) [][]string {
	t.Helper()
	paths, err := filepath.Glob(glob)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(pattern)
	var all [][]string
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, re.FindAllStringSubmatch(string(src), -1)...)
	}
	if len(all) == 0 {
		t.Fatalf("no match for %s in %s", pattern, glob)
	}
	return all
}
