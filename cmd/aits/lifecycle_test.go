package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not appear", path)
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// assertGone fails unless pid exits within a few seconds; it kills a survivor
// so it does not outlive the test.
func assertGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Errorf("process %d is still running", pid)
}

func TestSubmitTimeout(t *testing.T) {
	bin := vendor(t, "sleep")
	out := call{path: bin, args: submitArgs("--timeout", "1s", "--json", "hi")}.run(t)
	r := decode(t, out.stdout)
	if out.code != 1 || r.Status != "timeout" || r.SessionID != "sess-fake-1" || r.Message == "" {
		t.Fatalf("code=%d result=%+v", out.code, r)
	}
	assertGone(t, readPID(t, filepath.Join(bin, "child.pid")))
	assertGone(t, readPID(t, filepath.Join(bin, "grandchild.pid")))
}

func TestSubmitTimeoutEscalatesToKill(t *testing.T) {
	bin := vendor(t, "ignore_term")
	start := time.Now()
	out := call{path: bin, args: submitArgs("--timeout", "1s", "--json", "hi")}.run(t)
	if r := decode(t, out.stdout); out.code != 1 || r.Status != "timeout" {
		t.Fatalf("code=%d result=%+v", out.code, r)
	}
	if elapsed := time.Since(start); elapsed < 5*time.Second {
		t.Errorf("finished after %s; SIGKILL must wait for the 5s grace period", elapsed)
	}
	assertGone(t, readPID(t, filepath.Join(bin, "child.pid")))
}

func TestSubmitStopped(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			bin := vendor(t, "sleep")
			cmd, stdout, stderr := call{path: bin, args: submitArgs("--json", "hi")}.command(t)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitFile(t, filepath.Join(bin, "child.pid"))
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			out := finish(t, cmd.Wait(), stdout, stderr)
			r := decode(t, out.stdout)
			if out.code != 1 || r.Status != "stopped" || r.SessionID != "sess-fake-1" {
				t.Fatalf("code=%d result=%+v", out.code, r)
			}
			assertGone(t, readPID(t, filepath.Join(bin, "child.pid")))
			assertGone(t, readPID(t, filepath.Join(bin, "grandchild.pid")))
		})
	}
}

func TestSubmitOutputTooLarge(t *testing.T) {
	start := time.Now()
	out := call{path: vendor(t, "huge_line"), args: submitArgs("--json", "hi")}.run(t)
	r := decode(t, out.stdout)
	if out.code != 1 || r.Status != "output_too_large" || r.SessionID != "sess-fake-1" || r.Answer != "" {
		t.Fatalf("code=%d result=%+v", out.code, r)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("took %s; the agent must be terminated as soon as the limit is hit", elapsed)
	}
}

func TestSubmitLingeringGrandchild(t *testing.T) {
	bin := vendor(t, "linger")
	start := time.Now()
	out := call{path: bin, args: submitArgs("--json", "hi")}.run(t)
	if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" {
		t.Fatalf("code=%d result=%+v", out.code, r)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %s; a grandchild holding stdout must not keep aits waiting", elapsed)
	}
	assertGone(t, readPID(t, filepath.Join(bin, "grandchild.pid")))
}

// An agent that never reads its prompt, with a grandchild holding stdin, must
// not keep aits waiting: the timeout and the agent's exit both still bound it.
func TestSubmitUnreadPrompt(t *testing.T) {
	bin := vendor(t, "ignore_stdin")
	start := time.Now()
	out := call{path: bin, stdin: strings.Repeat("p", 1<<20), args: submitArgs("--timeout", "60s", "--json", "-")}.run(t)
	if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" {
		t.Fatalf("code=%d result=%+v", out.code, r)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("took %s; an unread prompt must not hold aits open", elapsed)
	}
	assertGone(t, readPID(t, filepath.Join(bin, "grandchild.pid")))
}

// Ctrl-C while aits is still reading the prompt from stdin stops it before any
// agent is started (P6).
func TestSubmitStoppedWhileReadingPrompt(t *testing.T) {
	bin := vendor(t, "ok")
	cmd, stdout, stderr := call{path: bin, args: submitArgs("--json", "-")}.command(t)
	cmd.Stdin = nil
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // let aits block on the prompt
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("aits ignored SIGINT while reading the prompt")
	}
	out := finish(t, err, stdout, stderr)
	if r := decode(t, out.stdout); out.code != 1 || r.Error == nil || r.Error.Code != "stopped" {
		t.Errorf("code=%d stdout=%q", out.code, out.stdout)
	}
	if ran(bin) {
		t.Errorf("the agent was started after the stop")
	}
}
