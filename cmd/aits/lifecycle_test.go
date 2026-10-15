package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// assertNoneRunning fails unless every process started from the fake agent
// in dir has exited within a few seconds, for a run whose agent may have been
// killed before it wrote its pid; it kills survivors.
func assertNoneRunning(t *testing.T, dir string) {
	t.Helper()
	pattern := regexp.QuoteMeta(dir)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		var exit *exec.ExitError
		switch err := exec.Command("pgrep", "-f", pattern).Run(); {
		case errors.As(err, &exit) && exit.ExitCode() == 1: // no match
			return
		case err != nil && exit == nil:
			t.Fatal(err)
		}
	}
	_ = exec.Command("pkill", "-KILL", "-f", pattern).Run()
	t.Errorf("a process started from %s is still running", dir)
}

func TestSubmitTimeout(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		bin := vendorAs(t, agent, "sleep")
		out := call{path: bin, args: agentArgs(agent, "--timeout", "1s", "--json", "hi")}.run(t)
		r := decode(t, out.stdout)
		if out.code != 1 || r.Status != "timeout" || r.SessionID != "sess-fake-1" || r.Message == "" {
			t.Fatalf("code=%d result=%+v", out.code, r)
		}
		assertGone(t, readPID(t, filepath.Join(bin, "child.pid")))
		assertGone(t, readPID(t, filepath.Join(bin, "grandchild.pid")))
	})
}

func TestSubmitTimeoutEscalatesToKill(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		bin := vendorAs(t, agent, "ignore_term")
		start := time.Now()
		out := call{path: bin, args: agentArgs(agent, "--timeout", "1s", "--json", "hi")}.run(t)
		if r := decode(t, out.stdout); out.code != 1 || r.Status != "timeout" {
			t.Fatalf("code=%d result=%+v", out.code, r)
		}
		if elapsed := time.Since(start); elapsed < 5*time.Second {
			t.Errorf("finished after %s; SIGKILL must wait for the 5s grace period", elapsed)
		}
		assertGone(t, readPID(t, filepath.Join(bin, "child.pid")))
		assertGone(t, readPID(t, filepath.Join(bin, "grandchild.pid")))
	})
}

// stopWith runs agent in scenario through aits submit with args, sends aits
// sigs once the agent has been running for delay, and checks that the agent's
// group is gone.
func stopWith(t *testing.T, agent, scenario string, delay time.Duration, sigs []syscall.Signal, args ...string) output {
	t.Helper()
	bin := vendorAs(t, agent, scenario)
	s := call{path: bin, args: agentArgs(agent, args...)}.start(t)
	waitFile(t, filepath.Join(bin, "child.pid"))
	time.Sleep(delay)
	s.signal(t, sigs...)
	out := s.waitOrKill(t, 10*time.Second)
	assertGone(t, readPID(t, filepath.Join(bin, "child.pid")))
	assertGone(t, readPID(t, filepath.Join(bin, "grandchild.pid")))
	return out
}

func TestSubmitStopped(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
			t.Run(sig.String(), func(t *testing.T) {
				out := stopWith(t, agent, "sleep", 0, []syscall.Signal{sig}, "--json", "hi")
				if r := decode(t, out.stdout); out.code != 1 || r.Status != "stopped" || r.SessionID != "sess-fake-1" || out.stderr != "" {
					t.Fatalf("code=%d result=%+v stderr=%q", out.code, r, out.stderr)
				}
			})
		}
		// The text rendering does not depend on the signal.
		t.Run("text", func(t *testing.T) {
			out := stopWith(t, agent, "sleep", 0, []syscall.Signal{syscall.SIGTERM}, "hi")
			if out.code != 1 || out.stdout != "" ||
				out.stderr != "aits: stopped: aits was stopped by a signal before the agent finished\nsession: sess-fake-1\n" {
				t.Fatalf("code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
			}
		})
	})
}

// A second signal during the grace period kills the group at once (P10).
func TestSubmitSecondSignalKills(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		start := time.Now()
		out := stopWith(t, agent, "ignore_term", 0, []syscall.Signal{syscall.SIGINT, syscall.SIGTERM}, "--json", "hi")
		if r := decode(t, out.stdout); out.code != 1 || r.Status != "stopped" || r.SessionID != "sess-fake-1" {
			t.Fatalf("code=%d result=%+v", out.code, r)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("finished after %s; a second signal must not wait for the 5s grace period", elapsed)
		}
	})
}

// A signal during a timeout's grace period still makes the run stopped, as
// spec §6 orders it, and does not cut the grace period short (#58).
func TestSubmitStoppedAfterTimeout(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		start := time.Now()
		// The agent wrote child.pid after it started, so the 1s timeout has
		// fired 3s later, and the grace period ends at 6s at the earliest.
		out := stopWith(t, agent, "ignore_term", 3*time.Second, []syscall.Signal{syscall.SIGINT}, "--timeout", "1s", "--json", "hi")
		if r := decode(t, out.stdout); out.code != 1 || r.Status != "stopped" || r.SessionID != "sess-fake-1" {
			t.Fatalf("code=%d result=%+v", out.code, r)
		}
		if elapsed := time.Since(start); elapsed < 5*time.Second {
			t.Errorf("finished after %s; one signal must not cut the 5s grace period short", elapsed)
		}
	})
}

// An agent that answered and exited keeps its answer when a timeout or a
// signal comes while aits waits for a descendant that still holds its output
// and ignores TERM (#81).
func TestSubmitAnswerKeptAfterExit(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		check := func(t *testing.T, bin string, out output) {
			t.Helper()
			if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" || r.Answer != "fake answer" {
				t.Errorf("code=%d result=%+v", out.code, r)
			}
			assertGone(t, readPID(t, filepath.Join(bin, "grandchild.pid")))
		}
		t.Run("timeout", func(t *testing.T) {
			bin := vendorAs(t, agent, "linger_ignore_term")
			check(t, bin, call{path: bin, args: agentArgs(agent, "--timeout", "1s", "--json", "hi")}.run(t))
		})
		t.Run("signal", func(t *testing.T) {
			bin := vendorAs(t, agent, "linger_ignore_term")
			s := call{path: bin, args: agentArgs(agent, "--json", "hi")}.start(t)
			waitFile(t, filepath.Join(bin, "child.pid"))
			waitExited(t, readPID(t, filepath.Join(bin, "child.pid")))
			s.signal(t, syscall.SIGINT)
			check(t, bin, s.waitOrKill(t, 15*time.Second))
		})
	})
}

// waitExited waits until pid has exited and been reaped, and a little longer
// for aits to see it.
func waitExited(t *testing.T, pid int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			time.Sleep(200 * time.Millisecond)
			return
		}
	}
	t.Fatalf("process %d did not exit", pid)
}

func TestSubmitOutputTooLarge(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		start := time.Now()
		out := call{path: vendorAs(t, agent, "huge_line"), args: agentArgs(agent, "--json", "hi")}.run(t)
		r := decode(t, out.stdout)
		if out.code != 1 || r.Status != "output_too_large" || r.SessionID != "sess-fake-1" || r.Answer != "" {
			t.Fatalf("code=%d result=%+v", out.code, r)
		}
		if elapsed := time.Since(start); elapsed > 15*time.Second {
			t.Errorf("took %s; the agent must be terminated as soon as the limit is hit", elapsed)
		}
	})
}

func TestSubmitLingeringGrandchild(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		bin := vendorAs(t, agent, "linger")
		start := time.Now()
		out := call{path: bin, args: agentArgs(agent, "--json", "hi")}.run(t)
		if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" {
			t.Fatalf("code=%d result=%+v", out.code, r)
		}
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Errorf("took %s; a grandchild holding stdout must not keep aits waiting", elapsed)
		}
		assertGone(t, readPID(t, filepath.Join(bin, "grandchild.pid")))
	})
}

// An agent that never reads its prompt, with a grandchild holding stdin, must
// not keep aits waiting: the timeout and the agent's exit both still bound it.
func TestSubmitUnreadPrompt(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		bin := vendorAs(t, agent, "ignore_stdin")
		start := time.Now()
		out := call{path: bin, stdin: strings.Repeat("p", 1<<20), args: agentArgs(agent, "--timeout", "60s", "--json", "-")}.run(t)
		if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" {
			t.Fatalf("code=%d result=%+v", out.code, r)
		}
		if elapsed := time.Since(start); elapsed > 15*time.Second {
			t.Errorf("took %s; an unread prompt must not hold aits open", elapsed)
		}
		assertGone(t, readPID(t, filepath.Join(bin, "grandchild.pid")))
	})
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
	// A second signal changes nothing.
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		if err := cmd.Process.Signal(sig); err != nil {
			t.Fatal(err)
		}
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
