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
// exit 2, never ignored.
func TestGrammarUnknownFlag(t *testing.T) {
	walk(cli.Root(), nil, func(path []string, _ *cli.Command) {
		args := append(slices.Clone(path), "--no-such-flag", "--json")
		out := call{args: args}.run(t)
		if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "unknown_flag" {
			t.Errorf("aits %s: code=%d stdout=%q", strings.Join(args, " "), out.code, out.stdout)
		}
	})
}

// TestGrammarUnknownCommand enforces P4 on every group: an unknown command
// is exit 2, never guessed and never sent to an agent.
func TestGrammarUnknownCommand(t *testing.T) {
	walk(cli.Root(), nil, func(path []string, c *cli.Command) {
		if c.Setup != nil {
			return
		}
		args := append(slices.Clone(path), "no-such-command", "--json")
		out := call{args: args}.run(t)
		if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "unknown_command" {
			t.Errorf("aits %s: code=%d stdout=%q", strings.Join(args, " "), out.code, out.stdout)
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
