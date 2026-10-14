package main

import (
	"slices"
	"strings"
	"testing"
)

// The answer is the last agent_message of the turn; earlier ones are progress.
func TestCodexAnswerIsLastMessage(t *testing.T) {
	out := call{path: vendorAs(t, "codex", "several_messages"), args: agentArgs("codex", "--json", "hi")}.run(t)
	if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" || r.Answer != "fake answer" {
		t.Fatalf("code=%d result=%+v", out.code, r)
	}
}

// Codex sends "error" events while it retries; they fail nothing on their own.
func TestCodexRetryNoticesAreNotFailures(t *testing.T) {
	out := call{path: vendorAs(t, "codex", "retry_errors"), args: agentArgs("codex", "--json", "hi")}.run(t)
	if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" || r.Answer != "fake answer" {
		t.Fatalf("code=%d result=%+v", out.code, r)
	}
}

// aits keeps Codex's refusal to run outside a git repository (P9).
func TestCodexKeepsGitRepoCheck(t *testing.T) {
	bin := vendorAs(t, "codex", "not_git")
	out := call{path: bin, args: agentArgs("codex", "--json", "hi")}.run(t)
	r := decode(t, out.stdout)
	if out.code != 1 || r.Status != "agent_failed" || r.ExitCode == nil || *r.ExitCode != 1 ||
		!strings.Contains(r.StderrTail, "--skip-git-repo-check was not specified") {
		t.Fatalf("code=%d result=%+v", out.code, r)
	}
	if args := readRecord(t, bin).Args; slices.Contains(args, "--skip-git-repo-check") {
		t.Errorf("aits passed --skip-git-repo-check: %q", args)
	}
}
