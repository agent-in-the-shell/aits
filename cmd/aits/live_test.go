//go:build live

package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The live tests run the real vendor CLIs (P12: opt-in, never in CI). They
// need a logged-in CLI on PATH and the real HOME, where its credentials live;
// AITS_HOME stays temporary. TestLiveClaude and TestLiveCodex spend one model
// call each.
//
//	go test -tags live ./cmd/aits -run TestLive

func TestLiveClaude(t *testing.T) { liveSubmit(t, "claude", t.TempDir(), "Reply with exactly: OK") }

// Codex refuses to run outside a git repository, and aits keeps that check.
func TestLiveCodex(t *testing.T) {
	work := t.TempDir()
	if b, err := exec.Command("git", "init", "-q", work).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, b)
	}
	liveSubmit(t, "codex", work, "Reply with exactly: OK")
}

// TestLiveResume continues a conversation with the session id the first run
// returned. It spends two model calls per agent.
func TestLiveResume(t *testing.T) {
	for _, agent := range agentNames {
		t.Run(agent, func(t *testing.T) {
			work := t.TempDir()
			if b, err := exec.Command("git", "init", "-q", work).CombinedOutput(); err != nil {
				t.Fatalf("git init: %v\n%s", err, b)
			}
			first := liveSubmit(t, agent, work, "Remember the word PELICAN. Reply with exactly: OK")
			second := liveSubmit(t, agent, work, "Which word did I ask you to remember? Reply with that word only.", "--session", first.SessionID)
			if second.SessionID != first.SessionID || !strings.Contains(strings.ToUpper(second.Answer), "PELICAN") {
				t.Fatalf("first %+v, second %+v", first, second)
			}
		})
	}
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
