//go:build live

package main

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
)

// TestLiveClaude runs the real Claude Code CLI (P12: opt-in, never in CI). It
// needs a logged-in claude on PATH and the real HOME, where its credentials
// live; AITS_HOME stays temporary. It spends one model call.
//
//	go test -tags live ./cmd/aits -run TestLive
func TestLiveClaude(t *testing.T) {
	cmd := exec.Command(aitsBin, "shell", "submit", "--agent", "claude", "--cwd", t.TempDir(),
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
