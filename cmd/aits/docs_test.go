package main

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/agent-in-the-shell/aits/internal/cli"
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
