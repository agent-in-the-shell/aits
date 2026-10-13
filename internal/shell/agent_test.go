package shell

import (
	"slices"
	"testing"
)

func TestClaudeEnviron(t *testing.T) {
	parent := []string{
		"PATH=/bin", "HOME=/h", "LC_ALL=C", "ANTHROPIC_API_KEY=k",
		"AITS_CANARY=secret", "GITHUB_TOKEN=t", "EXTRA=e", "NOEQUALS",
	}
	got := agents["claude"].environ(parent, []string{"EXTRA"})
	want := []string{"PATH=/bin", "HOME=/h", "LC_ALL=C", "ANTHROPIC_API_KEY=k", "EXTRA=e"}
	if !slices.Equal(got, want) {
		t.Errorf("environ = %q, want %q", got, want)
	}
}

func TestAgents(t *testing.T) {
	if got := Agents(); !slices.Equal(got, []string{"claude"}) {
		t.Errorf("Agents() = %q", got)
	}
}
