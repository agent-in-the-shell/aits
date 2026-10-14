//go:build live

package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestLive runs the real vendor CLIs (P12: opt-in, never in CI). It needs a
// logged-in CLI on PATH and the real HOME, where its credentials live;
// AITS_HOME stays temporary. Per agent it starts a conversation and continues
// it with the returned session id: two model calls each.
//
//	go test -tags live ./cmd/aits -run TestLive          # both agents
//	go test -tags live ./cmd/aits -run TestLive/codex    # one
func TestLive(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		work := gitRepo(t) // Codex refuses to run outside one, and aits keeps that check
		first := liveSubmit(t, agent, work, "Remember the word PELICAN. Reply with exactly: OK")
		second := liveSubmit(t, agent, work, "Which word did I ask you to remember? Reply with that word only.", "--session", first.SessionID)
		if second.SessionID != first.SessionID || !strings.Contains(strings.ToUpper(second.Answer), "PELICAN") {
			t.Fatalf("first %+v, second %+v", first, second)
		}
	})
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if b, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, b)
	}
	return dir
}

func liveSubmit(t *testing.T, agent, dir, prompt string, extra ...string) submitResult {
	t.Helper()
	args := append(agentArgs(agent, "--cwd", dir, "--timeout", "5m", "--json"), extra...)
	cmd := exec.Command(aitsBin, append(args, prompt)...)
	cmd.Env = append(os.Environ(), "AITS_HOME="+t.TempDir())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	out := finish(t, cmd.Run(), &stdout, &stderr)
	r := decode(t, out.stdout)
	if out.code != 0 || r.Status != "ok" || r.SessionID == "" || r.Answer == "" {
		t.Fatalf("code=%d result=%+v stderr=%q", out.code, r, out.stderr)
	}
	return r
}
