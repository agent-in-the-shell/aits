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
		"claude": {"PATH=/bin", "HOME=/h", "LC_ALL=C", "ANTHROPIC_API_KEY=a", "CLAUDE_CONFIG_DIR=/c", "EXTRA=e", "CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS=0"},
		"codex":  {"PATH=/bin", "HOME=/h", "LC_ALL=C", "OPENAI_API_KEY=o", "CODEX_HOME=/x", "CODEX_API_KEY=k", "CODEX_ACCESS_TOKEN=t", "EXTRA=e"},
	}
	for name, w := range want {
		if got := agents[name].environ(parent, []string{"EXTRA"}); !slices.Equal(got, w) {
			t.Errorf("%s environ = %q, want %q", name, got, w)
		}
	}
}

// TestEnvironDefaults: a default the caller passes takes the caller's value,
// and one only in aits's environment is not taken from it (P8).
func TestEnvironDefaults(t *testing.T) {
	const name = "CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS"
	parent := []string{"PATH=/bin", name + "=60000"}
	if got := agents["claude"].environ(parent, []string{name}); !slices.Equal(got, []string{"PATH=/bin", name + "=60000"}) {
		t.Errorf("passed: environ = %q", got)
	}
	if got := agents["claude"].environ(parent, nil); !slices.Equal(got, []string{"PATH=/bin", name + "=0"}) {
		t.Errorf("not passed: environ = %q", got)
	}
	if got := Environ(parent, nil); !slices.Equal(got, []string{"PATH=/bin"}) {
		t.Errorf("a job's command got %q", got)
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
