//go:build live

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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

// TestLiveWrite checks --write against the real vendors by the files left
// behind, never by the agent's answer (#104): nothing is written without
// it; with it, a file edit and a command write in the working directory, but
// not in a directory under the real $HOME, and a command reaches no network;
// a session resumed without it cannot write, and resumed with it can.
//
//	go test -tags live ./cmd/aits -run TestLiveWrite
func TestLiveWrite(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		work := gitRepo(t)
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		outside, err := os.MkdirTemp(home, ".aits-live-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(outside) })
		exists := func(name string) bool {
			_, err := os.Stat(filepath.Join(work, name))
			return err == nil
		}

		// The refusals are logged: a step that writes nothing passes whether or
		// not the agent tried.
		r := liveSubmit(t, agent, work, "Create a file named a.txt containing hi in the current directory. Reply DONE.")
		t.Logf("without --write: denied %q, answer %q", r.DeniedTools, r.Answer)
		if exists("a.txt") {
			t.Errorf("without --write, a.txt was written")
		}

		first := liveSubmit(t, agent, work, "Create a file named b.txt containing hi in the current directory with your file editing tool. "+
			"Then run this shell command exactly: sh -c 'echo ok > c.txt'. Reply DONE.", "--write")
		if !exists("b.txt") || !exists("c.txt") {
			t.Errorf("with --write: b.txt written %v, c.txt written %v", exists("b.txt"), exists("c.txt"))
		}

		r = liveSubmit(t, agent, work, "Run this shell command exactly: touch "+filepath.Join(outside, "d.txt")+
			". Then run this shell command exactly: sh -c \"curl -s -o /dev/null -w '%{http_code}' https://example.com > net.txt\". "+
			"Do not try any other way. Reply DONE.", "--write")
		net, _ := os.ReadFile(filepath.Join(work, "net.txt"))
		t.Logf("outside and network: net.txt %q, denied %q, answer %q", net, r.DeniedTools, r.Answer)
		if _, err := os.Stat(filepath.Join(outside, "d.txt")); err == nil {
			t.Errorf("with --write, a file was written under $HOME outside the working directory")
		}
		if strings.TrimSpace(string(net)) == "200" {
			t.Errorf("with --write, a command reached the network")
		}

		r = liveSubmit(t, agent, work, "Run this shell command exactly: sh -c 'echo ok > e.txt'. Do not try any other way. Reply DONE.",
			"--session", first.SessionID)
		t.Logf("resumed without --write: denied %q, answer %q", r.DeniedTools, r.Answer)
		if exists("e.txt") {
			t.Errorf("a session resumed without --write wrote e.txt")
		}
		liveSubmit(t, agent, work, "Run this shell command exactly: sh -c 'echo ok > f.txt'. Reply DONE.",
			"--session", first.SessionID, "--write")
		if !exists("f.txt") {
			t.Errorf("a session resumed with --write did not write f.txt")
		}
	})
}
