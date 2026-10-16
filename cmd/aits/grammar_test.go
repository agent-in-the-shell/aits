package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/agent-in-the-shell/aits/internal/cli"
)

// walk calls fn for c and every command below it; path excludes "aits".
func walk(c *cli.Command, path []string, fn func(path []string, c *cli.Command)) {
	fn(path, c)
	for _, ch := range c.Children {
		walk(ch, append(slices.Clone(path), ch.Name), fn)
	}
}

// TestGrammarUnknownFlag enforces P4 on every command: an unknown flag is
// exit 2, never ignored, and the command's help lists it (P5).
func TestGrammarUnknownFlag(t *testing.T) {
	walk(cli.Root(), nil, func(path []string, _ *cli.Command) {
		args := append(slices.Clone(path), "--no-such-flag", "--json")
		out := call{args: args}.run(t)
		if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "unknown_flag" || out.stderr != "" {
			t.Errorf("aits %s: code=%d stdout=%q stderr=%q", strings.Join(args, " "), out.code, out.stdout, out.stderr)
		}
		if got := helpErrors(t, path)["unknown_flag"]; got != "exit 2" {
			t.Errorf("aits %s --help lists unknown_flag with %q, want exit 2", strings.Join(path, " "), got)
		}
	})
}

// TestGrammarUnknownCommand enforces P4 on every group, and on help, which
// takes a command: an unknown command is exit 2, never guessed and never sent
// to an agent, and the help lists it (P5).
func TestGrammarUnknownCommand(t *testing.T) {
	walk(cli.Root(), nil, func(path []string, c *cli.Command) {
		if c.Setup != nil && c.Name != "help" {
			return
		}
		args := append(slices.Clone(path), "no-such-command", "--json")
		out := call{args: args}.run(t)
		if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "unknown_command" {
			t.Errorf("aits %s: code=%d stdout=%q", strings.Join(args, " "), out.code, out.stdout)
		}
		if got := helpErrors(t, path)["unknown_command"]; got != "exit 2" {
			t.Errorf("aits %s --help lists unknown_command with %q, want exit 2", strings.Join(path, " "), got)
		}
	})
}

func TestGrammarHelp(t *testing.T) {
	walk(cli.Root(), nil, func(path []string, _ *cli.Command) {
		out := call{args: append(slices.Clone(path), "--help")}.run(t)
		if out.code != 0 || !strings.HasPrefix(out.stdout, "Usage: aits") || out.stderr != "" {
			t.Errorf("aits %s --help: code=%d stdout=%q stderr=%q", strings.Join(path, " "), out.code, out.stdout, out.stderr)
		}
	})
}

func TestGrammarFlagsAnywhere(t *testing.T) {
	orders := [][]string{
		{"shell", "submit", "--agent", "claude", "--json", "hello"},
		{"shell", "submit", "hello", "--agent", "claude", "--json"},
		{"--json", "shell", "submit", "--agent=claude", "hello"},
	}
	for _, args := range orders {
		bin := vendor(t, "ok")
		out := call{path: bin, args: args}.run(t)
		if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" {
			t.Errorf("aits %s: code=%d stdout=%q", strings.Join(args, " "), out.code, out.stdout)
			continue
		}
		if got := readRecord(t, bin).Stdin; got != "hello" {
			t.Errorf("aits %s: prompt %q", strings.Join(args, " "), got)
		}
	}
}

func TestGrammarDoubleDash(t *testing.T) {
	bin := vendor(t, "ok")
	out := call{path: bin, args: submitArgs("--json", "--", "--not-a-flag")}.run(t)
	if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" || readRecord(t, bin).Stdin != "--not-a-flag" {
		t.Errorf("code=%d stdout=%q", out.code, out.stdout)
	}
	bin = vendor(t, "ok")
	out = call{path: bin, args: submitArgs("--", "--json")}.run(t)
	if out.code != 0 || out.stdout != "fake answer\n" || readRecord(t, bin).Stdin != "--json" {
		t.Errorf("--json after -- must be the prompt: code=%d stdout=%q", out.code, out.stdout)
	}
}

func TestGrammarLeafFlagBeforeCommand(t *testing.T) {
	out := call{args: []string{"shell", "--agent", "claude", "submit", "hi", "--json"}}.run(t)
	if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "unknown_flag" {
		t.Errorf("code=%d stdout=%q", out.code, out.stdout)
	}
}

// #85: a flag that takes one value is refused when given twice, rather than
// the last one silently winning; --env still repeats.
func TestGrammarRepeatedFlag(t *testing.T) {
	for _, args := range [][]string{
		{"shell", "submit", "--agent", "claude", "--agent", "codex", "hi"},
		{"task", "create", "--agent", "claude", "--agent", "codex", "hi"},
		{"session", "search", "--agent", "claude", "--agent=codex", "x"},
	} {
		out := call{args: append(args, "--json")}.run(t)
		if e := decode(t, out.stdout).Error; out.code != 2 || e == nil || e.Code != "invalid_input" || e.Message != "flag --agent is given more than once" {
			t.Errorf("aits %s: code=%d stdout=%q", strings.Join(args, " "), out.code, out.stdout)
		}
	}
}

// #85: --help prints help only once the whole line parses: an unknown flag or
// a bad value is a usage error wherever it is, before or after --help, at a
// group or at a leaf.
func TestGrammarHelpChecksTheLine(t *testing.T) {
	for _, args := range []string{
		"task list --help --bogus",
		"task list --bogus --help",
		"task --json=notabool",
		"task --help --json=notabool",
		"task list --json=notabool --help",
	} {
		out := call{args: strings.Fields(args)}.run(t)
		if out.code != 2 || out.stdout != "" || !strings.HasPrefix(out.stderr, "aits: ") {
			t.Errorf("aits %s: code=%d stdout=%q stderr=%q, want a usage error", args, out.code, out.stdout, out.stderr)
		}
	}
}

// #85: once --json appears before "--", errors are the JSON envelope on
// stdout, even when another flag took --json as its value (shell-submit spec
// §4).
func TestGrammarJSONTakenAsValue(t *testing.T) {
	for _, args := range [][]string{
		{"shell", "submit", "--agent", "--json", "hi"},
		{"shell", "submit", "--agent", "claude", "--timeout", "--json", "hi"},
		{"task", "create", "--after", "--json", "hi"},
	} {
		out := call{args: args}.run(t)
		if e := decode(t, out.stdout).Error; out.code != 2 || e == nil || e.Code != "invalid_input" || out.stderr != "" {
			t.Errorf("aits %s: code=%d stdout=%q stderr=%q", strings.Join(args, " "), out.code, out.stdout, out.stderr)
		}
	}
}
