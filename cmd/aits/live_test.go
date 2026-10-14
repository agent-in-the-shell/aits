//go:build live

package main

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
)

// The live tests run the real vendor CLIs (P12: opt-in, never in CI). They
// need a logged-in CLI on PATH and the real HOME, where its credentials live;
// AITS_HOME stays temporary. Each spends one model call.
//
//	go test -tags live ./cmd/aits -run TestLive

func TestLiveClaude(t *testing.T) { liveSubmit(t, "claude", t.TempDir()) }

// Codex refuses to run outside a git repository, and aits keeps that check.
func TestLiveCodex(t *testing.T) {
	work := t.TempDir()
	if b, err := exec.Command("git", "init", "-q", work).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, b)
	}
	liveSubmit(t, "codex", work)
}

func liveSubmit(t *testing.T, agent, dir string) {
	t.Helper()
	cmd := exec.Command(aitsBin, "shell", "submit", "--agent", agent, "--cwd", dir,
		"--timeout", "5m", "--json", "Reply with exactly: OK")
	cmd.Env = append(os.Environ(), "AITS_HOME="+t.TempDir())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	out := finish(t, cmd.Run(), &stdout, &stderr)
	r := decode(t, out.stdout)
	if out.code != 0 || r.Status != "ok" || r.SessionID == "" || r.Answer == "" {
		t.Fatalf("code=%d result=%+v stderr=%q", out.code, r, out.stderr)
	}
}
