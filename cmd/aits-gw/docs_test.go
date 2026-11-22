package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/agent-in-the-shell/aits/internal/cli"
	"github.com/agent-in-the-shell/aits/internal/gw"
)

var update = flag.Bool("update", false, "rewrite docs/gw-cli.md from the binary's help output")

// walk calls fn for c and every command below it; path excludes "aits-gw".
func walk(c *cli.Command, path []string, fn func(path []string, c *cli.Command)) {
	fn(path, c)
	for _, ch := range c.Children {
		walk(ch, append(slices.Clone(path), ch.Name), fn)
	}
}

func help(t *testing.T, path []string) string {
	t.Helper()
	out := newHome(t).run(append(slices.Clone(path), "--help")...)
	if out.code != 0 || out.stderr != "" || !strings.HasPrefix(out.stdout, "Usage: aits-gw") {
		t.Fatalf("aits-gw %s --help: code=%d stdout=%q stderr=%q", strings.Join(path, " "), out.code, out.stdout, out.stderr)
	}
	return out.stdout
}

// TestCLIDocs enforces P13: docs/gw-cli.md is exactly the help the binary
// prints.
func TestCLIDocs(t *testing.T) {
	var b strings.Builder
	b.WriteString("# aits-gw CLI reference\n\nGenerated from the help output of `aits-gw` by\n" +
		"`go test ./cmd/aits-gw -run TestCLIDocs -update`. Do not edit by hand.\n")
	walk(gw.Root(), nil, func(path []string, _ *cli.Command) {
		fmt.Fprintf(&b, "\n## %s\n\n```text\n%s```\n", strings.Join(append([]string{"aits-gw"}, path...), " "), help(t, path))
	})
	const file = "../../docs/gw-cli.md"
	if *update {
		if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v; run: go test ./cmd/aits-gw -run TestCLIDocs -update", err)
	}
	if string(want) != b.String() {
		t.Fatalf("docs/gw-cli.md is out of date; run: go test ./cmd/aits-gw -run TestCLIDocs -update")
	}
}

// TestGrammar enforces P4 and P5 on every aits-gw command, as aits's own
// grammar tests do: help exits 0, an unknown flag is exit 2 unknown_flag as
// JSON on stdout, a group refuses an unknown command, every command accepts
// --json, and help lists the grammar errors it can produce.
func TestGrammar(t *testing.T) {
	walk(gw.Root(), nil, func(path []string, c *cli.Command) {
		name := strings.Join(append([]string{"aits-gw"}, path...), " ")
		text := help(t, path)
		documented := helpErrors(text)
		wantError(t, name+" --no-such-flag", newHome(t).run(append(slices.Clone(path), "--no-such-flag", "--json")...), 2, "unknown_flag")
		if documented["unknown_flag"] != "exit 2" {
			t.Errorf("%s --help does not list unknown_flag with exit 2: %v", name, documented)
		}
		if c.Setup == nil || c.Name == "help" {
			wantError(t, name+" no-such-command", newHome(t).run(append(slices.Clone(path), "no-such-command", "--json")...), 2, "unknown_command")
			if documented["unknown_command"] != "exit 2" {
				t.Errorf("%s --help does not list unknown_command with exit 2: %v", name, documented)
			}
		} else if !strings.Contains(text, "\n  --json ") {
			t.Errorf("%s --help has no --json flag", name)
		}
	})
}

// TestHelpDocumentsErrors enforces P5 across the tree: every error code
// internal/gw can produce is listed, with the exit code it causes, in at
// least one command's help, and every code a help lists is produced with
// that exit code. Which command lists which code is review-only.
func TestHelpDocumentsErrors(t *testing.T) {
	exit := map[string]string{"UsageError": "exit 2", "Failure": "exit 1"}
	produced := map[string]string{"unknown_flag": "exit 2", "unknown_command": "exit 2"}
	paths, err := filepath.Glob("../../internal/gw/*.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`\bcli\.(UsageError|Failure)\(\s*"(\w+)"`)
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			produced[m[2]] = exit[m[1]]
		}
	}
	listed := map[string]string{}
	walk(gw.Root(), nil, func(path []string, _ *cli.Command) {
		for code, ex := range helpErrors(help(t, path)) {
			if produced[code] != ex {
				t.Errorf("aits-gw %s --help lists %s with %s; the source produces it with %q", strings.Join(path, " "), code, ex, produced[code])
			}
			listed[code] = ex
		}
	})
	for code, ex := range produced {
		if listed[code] != ex {
			t.Errorf("error code %s (%s) is in no command's help", code, ex)
		}
	}
}

// helpErrors maps each error code on a help's "Errors: a, b (exit 2); c
// (exit 1)." line, which may wrap, to its exit code.
func helpErrors(help string) map[string]string {
	errs := map[string]string{}
	_, after, ok := strings.Cut(help, "\nErrors: ")
	if !ok {
		return errs
	}
	after, _, _ = strings.Cut(after, ".\n")
	after = strings.Join(strings.Fields(after), " ")
	for _, group := range strings.Split(after, "; ") {
		codes, ex, _ := strings.Cut(strings.TrimSuffix(group, ")"), " (")
		for _, c := range strings.Split(codes, ", ") {
			errs[c] = ex
		}
	}
	return errs
}

// TestSkill enforces P13 for aits-gw: skills/aits/SKILL.md names every
// aits-gw command, every flag of each, and every status its help documents
// in a "status":"…" result (P5).
func TestSkill(t *testing.T) {
	src, err := os.ReadFile("../../skills/aits/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	skill := string(src)
	flagRow := regexp.MustCompile(`(?m)^  (--[\w-]+)`)
	status := regexp.MustCompile(`"status":"([\w|]+)"`)
	statuses := 0
	walk(gw.Root(), nil, func(path []string, _ *cli.Command) {
		name := strings.Join(append([]string{"aits-gw"}, path...), " ")
		if !strings.Contains(skill, "`"+name) {
			t.Errorf("the skill does not name %q", name)
		}
		text := help(t, path)
		for _, m := range flagRow.FindAllStringSubmatch(text, -1) {
			if !regexp.MustCompile(m[1] + `\b`).MatchString(skill) {
				t.Errorf("the skill does not name %s's flag %s", name, m[1])
			}
		}
		for _, m := range status.FindAllStringSubmatch(text, -1) {
			for _, s := range strings.Split(m[1], "|") {
				statuses++
				if !strings.Contains(skill, "`"+s+"`") {
					t.Errorf("the skill does not name %s's status %q", name, s)
				}
			}
		}
	})
	if statuses == 0 {
		t.Fatal(`no "status":"…" in any aits-gw help`)
	}
}
