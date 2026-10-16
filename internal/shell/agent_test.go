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
	const sandbox = `{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true,"allowUnsandboxedCommands":false,"failIfUnavailable":true}}`
	claude := []string{"-p", "--output-format", "stream-json", "--verbose"}
	claudeWrite := append(slices.Clip(claude), "--permission-mode", "acceptEdits", "--settings", sandbox)
	tests := []struct {
		agent, session string
		write          bool
		want           []string
	}{
		{"claude", "", false, claude},
		{"claude", "s1", false, append(slices.Clip(claude), "--resume", "s1")},
		{"claude", "", true, claudeWrite},
		{"claude", "s1", true, append(slices.Clip(claudeWrite), "--resume", "s1")},
		{"codex", "", false, []string{"exec", "--json", "-s", "read-only", "-"}},
		{"codex", "s1", false, []string{"exec", "--json", "-s", "read-only", "resume", "s1", "-"}},
		{"codex", "", true, []string{"exec", "--json", "-s", "workspace-write", "-"}},
		{"codex", "s1", true, []string{"exec", "--json", "-s", "workspace-write", "resume", "s1", "-"}},
	}
	for _, tt := range tests {
		if got := agents[tt.agent].argv(tt.session, tt.write); !slices.Equal(got, tt.want) {
			t.Errorf("%s argv(%q, %v) = %q, want %q", tt.agent, tt.session, tt.write, got, tt.want)
		}
	}
}
