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

func TestArgv(t *testing.T) {
	tests := []struct {
		agent, session string
		want           []string
	}{
		{"claude", "", []string{"-p", "--output-format", "stream-json", "--verbose"}},
		{"claude", "s1", []string{"-p", "--output-format", "stream-json", "--verbose", "--resume", "s1"}},
		{"codex", "", []string{"exec", "--json", "-"}},
		{"codex", "s1", []string{"exec", "--json", "resume", "s1", "-"}},
	}
	for _, tt := range tests {
		if got := agents[tt.agent].argv(tt.session); !slices.Equal(got, tt.want) {
			t.Errorf("%s argv(%q) = %q, want %q", tt.agent, tt.session, got, tt.want)
		}
	}
}
