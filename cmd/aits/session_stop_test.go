package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// started is an aits process a test is watching.
type started struct {
	cmd            *exec.Cmd
	stdout, stderr *bytes.Buffer
	done           chan error
}

func (c call) start(t *testing.T) *started {
	t.Helper()
	cmd, stdout, stderr := c.command(t)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &started{cmd: cmd, stdout: stdout, stderr: stderr, done: make(chan error, 1)}
	go func() { s.done <- cmd.Wait() }()
	return s
}

// wait waits up to d for aits to exit; ok is false if it is still running.
func (s *started) wait(t *testing.T, d time.Duration) (out output, ok bool) {
	t.Helper()
	select {
	case err := <-s.done:
		return finish(t, err, s.stdout, s.stderr), true
	case <-time.After(d):
		return output{}, false
	}
}

// waitOrKill is wait that kills aits and fails the test when it is still
// running after d.
func (s *started) waitOrKill(t *testing.T, d time.Duration, what string) output {
	t.Helper()
	out, ok := s.wait(t, d)
	if !ok {
		_ = s.cmd.Process.Kill()
		<-s.done
		t.Fatalf("%s: aits did not exit within %s", what, d)
	}
	return out
}

// runWithin runs c and fails the test if aits has not exited after d.
func runWithin(t *testing.T, c call, d time.Duration) output {
	t.Helper()
	return c.start(t).waitOrKill(t, d, "aits "+strings.Join(c.args, " "))
}

// oneSession builds a HOME with one session of agent that contains "needle"
// and returns the HOME and the session's "agent:id".
func oneSession(t *testing.T, agent string) (string, string) {
	home := t.TempDir()
	if agent == "codex" {
		writeTranscript(t, home, ".codex/sessions/2026/10/14/rollout-x-c1.jsonl", time.Now(),
			`{"type":"session_meta","payload":{"id":"c1","cwd":"/w"}}`,
			`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello needle"}]}}`)
		return home, "codex:c1"
	}
	writeTranscript(t, home, ".claude/projects/-w/s1.jsonl", time.Now(),
		`{"type":"user","sessionId":"s1","cwd":"/w","message":{"content":"hello needle"}}`)
	return home, "claude:s1"
}

// A path that matches the transcript pattern but is not a regular file is
// counted as skipped and never read: a pipe would block the search, a device
// would never end it.
func TestSessionSearchSkipsNonRegularFiles(t *testing.T) {
	tests := []struct {
		name, agent string
		rel         string // the special path under HOME
		target      string // a symlink to this, or a FIFO when empty
	}{
		{"claude fifo", "claude", ".claude/projects/-w/pipe.jsonl", ""},
		{"codex fifo", "codex", ".codex/sessions/2026/10/14/rollout-x.jsonl", ""},
		{"symlink to a device", "claude", ".claude/projects/-w/dev.jsonl", "/dev/zero"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home, want := oneSession(t, tt.agent)
			special := filepath.Join(home, tt.rel)
			if tt.target == "" {
				if err := syscall.Mkfifo(special, 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(tt.target, special); err != nil {
				t.Fatal(err)
			}
			env := []string{"HOME=" + home}
			out := runWithin(t, call{env: env, args: []string{"session", "search", "--json", "needle"}}, 5*time.Second)
			o := decodeSearch(t, out.stdout)
			if out.code != 0 || !slices.Equal(found(o), []string{want}) || o.Skipped.Files != 1 || o.Skipped.Lines != 0 {
				t.Fatalf("code=%d results=%q skipped=%+v", out.code, found(o), o.Skipped)
			}
			out = runWithin(t, call{env: env, args: []string{"session", "search", "needle"}}, 5*time.Second)
			_, id, _ := strings.Cut(want, ":")
			if out.code != 0 || !strings.Contains(out.stdout, "  "+id+"  ") ||
				out.stderr != "aits: skipped 1 files and 0 lines that could not be read\n" {
				t.Errorf("text mode: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
			}
			if tt.target == "" {
				assertFifoUnread(t, special, env)
			}
		})
	}
}

// assertFifoUnread puts one byte into the pipe, runs a search, and checks the
// byte is still there: the search did not read from the pipe.
func assertFifoUnread(t *testing.T, fifo string, env []string) {
	t.Helper()
	f, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write([]byte{'z'}); err != nil {
		t.Fatal(err)
	}
	out := runWithin(t, call{env: env, args: []string{"session", "search", "--json", "needle"}}, 5*time.Second)
	if o := decodeSearch(t, out.stdout); out.code != 0 || o.Skipped.Files != 1 {
		t.Fatalf("with a writer: code=%d skipped=%+v", out.code, o.Skipped)
	}
	// A non-blocking read takes the byte if it is still there and returns
	// EAGAIN at once if the search took it (deadlines do not work on FIFOs
	// on every platform).
	fd := int(f.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1)
	if n, err := syscall.Read(fd, b); n != 1 || b[0] != 'z' {
		t.Errorf("the byte in the pipe was taken by the search: n=%d err=%v", n, err)
	}
}

func TestSessionSearchFollowsSymlinkToTranscript(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	writeTranscript(t, dir, "kept.jsonl", time.Now(), `{"type":"user","sessionId":"l1","cwd":"/w","message":{"content":"needle"}}`)
	elsewhere := filepath.Join(dir, "kept.jsonl")
	if err := os.MkdirAll(filepath.Join(home, ".claude/projects/-w"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, ".claude/projects/-w/link.jsonl")); err != nil {
		t.Fatal(err)
	}
	out := call{env: []string{"HOME=" + home}, args: []string{"session", "search", "--json", "needle"}}.run(t)
	if o := decodeSearch(t, out.stdout); out.code != 0 || !slices.Equal(found(o), []string{"claude:l1"}) || o.Skipped.Files != 0 {
		t.Fatalf("code=%d results=%q skipped=%+v", out.code, found(o), o.Skipped)
	}
}

// bigTranscript writes a Claude transcript of at least size bytes whose only
// line with "needle" is the last.
func bigTranscript(t *testing.T, size int) string {
	t.Helper()
	home := t.TempDir()
	p := filepath.Join(home, ".claude/projects/-w/big.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	line := `{"type":"user","sessionId":"big","cwd":"/w","message":{"content":"` + strings.Repeat("x", 1000) + `"}}` + "\n"
	for written := 0; written < size; written += len(line) {
		if _, err := w.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.WriteString(`{"type":"user","sessionId":"big","cwd":"/w","message":{"content":"needle"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return home
}

// A signal stops the search within a second even in the middle of a large
// file (P10 by analogy), and the stop is reported as an error, never as a
// partial result (P6).
func TestSessionSearchStopsPromptly(t *testing.T) {
	home := bigTranscript(t, 256<<20)
	for _, tt := range []struct {
		sig    syscall.Signal
		json   bool
		stdout string
		stderr string
	}{
		{syscall.SIGINT, true, `{"error":{"code":"stopped","message":"stopped by a signal"}}` + "\n", ""},
		{syscall.SIGTERM, false, "", "aits: stopped by a signal\n"},
	} {
		t.Run(tt.sig.String(), func(t *testing.T) {
			args := []string{"session", "search", "needle"}
			if tt.json {
				args = append(args, "--json")
			}
			s := call{env: []string{"HOME=" + home}, args: args}.start(t)
			if _, ok := s.wait(t, 200*time.Millisecond); ok {
				t.Fatal("the search finished before the signal; the fixture is too small to test stopping")
			}
			if err := s.cmd.Process.Signal(tt.sig); err != nil {
				t.Fatal(err)
			}
			out := s.waitOrKill(t, time.Second, "after "+tt.sig.String())
			if out.code != 1 || out.stdout != tt.stdout || out.stderr != tt.stderr {
				t.Errorf("code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
			}
		})
	}
	// Without a signal the same search still finds the last line.
	out := call{env: []string{"HOME=" + home}, args: []string{"session", "search", "--json", "needle"}}.run(t)
	if o := decodeSearch(t, out.stdout); out.code != 0 || !slices.Equal(found(o), []string{"claude:big"}) {
		t.Errorf("uncancelled: code=%d results=%q", out.code, found(o))
	}
}
