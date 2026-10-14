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
// documented in help: every result field, status and error code the code can
// produce has a row in "aits shell submit --help", and each error code's row
// names the exit code it causes.
func TestSubmitHelpDocumentsOutput(t *testing.T) {
	rows := map[string]string{} // first word of each indented help line -> line
	for _, line := range strings.Split(call{args: []string{"shell", "submit", "--help"}}.run(t).stdout, "\n") {
		if f := strings.Fields(line); strings.HasPrefix(line, "  ") && len(f) > 0 {
			rows[f[0]] += line + "\n"
		}
	}
	rt := reflect.TypeFor[shell.Result]()
	for i := range rt.NumField() {
		if name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ","); rows[name] == "" {
			t.Errorf("result field %q is not documented in help", name)
		}
	}
	for _, m := range sourceMatches(t, "../../internal/shell", `\bStatus\s*=\s*"(\w+)"`) {
		if rows[m[1]] == "" {
			t.Errorf("status %q is not documented in help", m[1])
		}
	}
	exit := map[string]string{"usageError": "exit 2", "failure": "exit 1"}
	for _, m := range sourceMatches(t, "../../internal/cli", `\b(usageError|failure)\(\s*"(\w+)"`) {
		if !strings.Contains(rows[m[2]], exit[m[1]]) {
			t.Errorf("error code %q is not documented in help with %s", m[2], exit[m[1]])
		}
	}
}

// sourceMatches returns the submatches of pattern in the non-test Go files of
// dir, failing the test if there are none.
func sourceMatches(t *testing.T, dir, pattern string) [][]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
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
		t.Fatalf("no match for %s in %s", pattern, dir)
	}
	return all
}
