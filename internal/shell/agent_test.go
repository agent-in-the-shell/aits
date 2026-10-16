package shell

import (
	"slices"
	"testing"
)

func TestEnviron(t *testing.T) {
	parent := []string{
		"PATH=/bin", "HOME=/h", "LC_ALL=C",
		"ANTHROPIC_API_KEY=a", "CLAUDE_CONFIG_DIR=/c", "OPENAI_API_KEY=o", "CODEX_HOME=/x", "CODEX_API_KEY=k", "CODEX_ACCESS_TOKEN=t",
		"AITS_CANARY=secret", "GITHUB_TOKEN=t", "EXTRA=e", "NOEQUALS",
	}
	want := map[string][]string{
		"claude": {"PATH=/bin", "HOME=/h", "LC_ALL=C", "ANTHROPIC_API_KEY=a", "CLAUDE_CONFIG_DIR=/c", "EXTRA=e"},
		"codex":  {"PATH=/bin", "HOME=/h", "LC_ALL=C", "OPENAI_API_KEY=o", "CODEX_HOME=/x", "CODEX_API_KEY=k", "CODEX_ACCESS_TOKEN=t", "EXTRA=e"},
	}
	for name, w := range want {
		if got := agents[name].environ(parent, []string{"EXTRA"}); !slices.Equal(got, w) {
			t.Errorf("%s environ = %q, want %q", name, got, w)
		}
	}
}

func TestAgents(t *testing.T) {
	if got := Agents(); !slices.Equal(got, []string{"claude", "codex"}) {
		t.Errorf("Agents() = %q", got)
	}
	for name, ag := range agents {
		if ag.name != name || ag.parse == nil {
			t.Errorf("agent %q is misconfigured: %+v", name, ag)
		}
	}
}
