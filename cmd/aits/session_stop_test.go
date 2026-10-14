package main

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runWithin runs c and fails the test if aits has not exited after d.
func runWithin(t *testing.T, c call, d time.Duration) output {
	t.Helper()
	cmd, stdout, stderr := c.command(t)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return finish(t, err, stdout, stderr)
	case <-time.After(d):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("aits %s did not exit within %s", strings.Join(c.args, " "), d)
		return output{}
	}
}

// oneSession builds a HOME with one Claude and one Codex session that both
// contain "needle".
func oneSession(t *testing.T) string {
	home := t.TempDir()
	now := time.Now()
	writeTranscript(t, home, ".claude/projects/-w/s1.jsonl", now,
		`{"type":"user","sessionId":"s1","cwd":"/w","message":{"content":"hello needle"}}`)
	writeTranscript(t, home, ".codex/sessions/2026/10/14/rollout-x-c1.jsonl", now,
		`{"type":"session_meta","payload":{"id":"c1","cwd":"/w"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello needle"}]}}`)
	return home
}

// A path that matches the transcript pattern but is not a regular file is
// counted as skipped and never read: a pipe would block the search, a device
// would never end it.
func TestSessionSearchSkipsNonRegularFiles(t *testing.T) {
	tests := []struct {
		name string
		make func(t *testing.T, home string) string // returns the special path
	}{
		{"claude fifo", func(t *testing.T, home string) string {
			p := filepath.Join(home, ".claude/projects/-w/pipe.jsonl")
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{"codex fifo", func(t *testing.T, home string) string {
			p := filepath.Join(home, ".codex/sessions/2026/10/14/rollout-x.jsonl")
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{"symlink to a device", func(t *testing.T, home string) string {
			p := filepath.Join(home, ".claude/projects/-w/dev.jsonl")
			if err := os.Symlink("/dev/zero", p); err != nil {
				t.Fatal(err)
			}
			return p
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := oneSession(t)
			special := tt.make(t, home)
			env := []string{"HOME=" + home}
			out := runWithin(t, call{env: env, args: []string{"session", "search", "--json", "needle"}}, 5*time.Second)
			o := decodeSearch(t, out.stdout)
			if out.code != 0 || !slices.Equal(found(o), []string{"codex:c1", "claude:s1"}) /* same mtime: ties go by session id */ || o.Skipped.Files != 1 || o.Skipped.Lines != 0 {
				t.Fatalf("code=%d results=%q skipped=%+v", out.code, found(o), o.Skipped)
			}
			out = runWithin(t, call{env: env, args: []string{"session", "search", "needle"}}, 5*time.Second)
			if out.code != 0 || out.stderr != "aits: skipped 1 files and 0 lines that could not be read\n" {
				t.Errorf("text mode: code=%d stderr=%q", out.code, out.stderr)
			}
			if fi, err := os.Lstat(special); err == nil && fi.Mode()&os.ModeNamedPipe != 0 {
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
	home := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "kept.jsonl")
	if err := os.WriteFile(elsewhere, []byte(`{"type":"user","sessionId":"l1","cwd":"/w","message":{"content":"needle"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude/projects/-w"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, ".claude/projects/-w/link.jsonl")); err != nil {
		t.Fatal(err)
	}
	out := call{env: []string{"HOME=" + home}, args: []string{"session", "search", "--json", "needle"}}.run(t)
	if o := decodeSearch(t, out.stdout); !slices.Equal(found(o), []string{"claude:l1"}) || o.Skipped.Files != 0 {
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
			cmd, stdout, stderr := call{env: []string{"HOME=" + home}, args: args}.command(t)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
				t.Fatal("the search finished before the signal; the fixture is too small to test stopping")
			case <-time.After(200 * time.Millisecond):
			}
			if err := cmd.Process.Signal(tt.sig); err != nil {
				t.Fatal(err)
			}
			var err error
			select {
			case err = <-done:
			case <-time.After(time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Fatalf("aits did not stop within 1s of %s", tt.sig)
			}
			out := finish(t, err, stdout, stderr)
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
