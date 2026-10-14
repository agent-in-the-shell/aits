package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// stderrTailSize is how much of the agent's stderr is kept for agent_failed.
const stderrTailSize = 4 << 10

const (
	// killGrace is the time between SIGTERM and SIGKILL to the group (P10).
	killGrace = 5 * time.Second
	// drainGrace is how long reads wait for the pipes to close after SIGKILL.
	drainGrace = time.Second
)

var errTimedOut = errors.New("agent timed out")

// run starts the agent in its own process group and supervises it until it
// has exited and its output pipes are closed.
func run(ctx context.Context, ag agent, path string, req Request) (observation, error) {
	outR, outW, err := os.Pipe()
	if err != nil {
		return observation{}, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return observation{}, err
	}
	defer outR.Close()
	defer errR.Close()
	cmd := exec.Command(path, ag.args...)
	cmd.Dir = req.Dir
	cmd.Env = ag.environ(req.Environ, req.Pass)
	cmd.Stdin = strings.NewReader(req.Prompt)
	cmd.Stdout, cmd.Stderr = outW, errW
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Wait also waits for the copy of the prompt to stdin. If the agent never
	// reads it and a descendant holds stdin open, give up drainGrace after the
	// agent exits instead of waiting for that descendant (P10).
	cmd.WaitDelay = drainGrace
	err = cmd.Start()
	outW.Close()
	errW.Close()
	if err != nil {
		return observation{}, fmt.Errorf("start %s: %w", ag.name, err)
	}
	return supervise(ctx, cmd, outR, errR, req.Timeout, ag.parse), nil
}

// supervise waits for three things: the agent to exit, its stdout to reach
// EOF, and its stderr to reach EOF. A timeout, a cancelled ctx, an oversized
// event, or the agent's own exit terminates the whole group, so nothing the
// agent started outlives it (P10).
func supervise(ctx context.Context, cmd *exec.Cmd, out, errOut *os.File, timeout time.Duration, parse parser) observation {
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, errTimedOut)
	defer cancel()
	g := &group{pgid: cmd.Process.Pid, pipes: []*os.File{out, errOut}}

	evCh := make(chan events, 1)
	go func() { evCh <- readEvents(out, parse) }()
	tail := &tailBuffer{max: stderrTailSize}
	tailCh := make(chan struct{}, 1)
	go func() { _, _ = io.Copy(tail, errOut); tailCh <- struct{}{} }()
	waitCh := make(chan struct{}, 1)
	go func() { _ = cmd.Wait(); waitCh <- struct{}{} }()

	var o observation
	done := ctx.Done()
	for pending := 3; pending > 0; {
		select {
		case <-waitCh:
			pending--
			o.exitCode = cmd.ProcessState.ExitCode()
			g.terminate()
		case o.events = <-evCh:
			pending--
			if o.events.tooLarge {
				g.terminate()
			}
		case <-tailCh:
			pending--
		case <-done:
			done = nil
			if errors.Is(context.Cause(ctx), errTimedOut) {
				o.timedOut = true
			} else {
				o.stopped = true
			}
			g.terminate()
		}
	}
	g.settle()
	o.stderrTail = tail.String()
	return o
}

// group is the agent's process group (P10).
type group struct {
	pgid     int
	pipes    []*os.File
	once     sync.Once
	kill     *time.Timer
	deadline time.Time // when reads give up; zero until terminate
}

// terminate sends SIGTERM to the group, SIGKILL killGrace later, and makes
// reads from the pipes give up shortly after that, so that a process that left
// the group cannot hold aits open.
func (g *group) terminate() {
	g.once.Do(func() {
		g.deadline = time.Now().Add(killGrace + drainGrace)
		_ = syscall.Kill(-g.pgid, syscall.SIGTERM)
		g.kill = time.AfterFunc(killGrace, func() { _ = syscall.Kill(-g.pgid, syscall.SIGKILL) })
		for _, p := range g.pipes {
			_ = p.SetReadDeadline(g.deadline)
		}
	})
}

// settle waits, until the drain deadline at most, for the group to be empty,
// and then cancels the pending SIGKILL so it cannot hit a reused group id.
func (g *group) settle() {
	if g.deadline.IsZero() {
		return
	}
	for time.Now().Before(g.deadline) {
		if errors.Is(syscall.Kill(-g.pgid, 0), syscall.ESRCH) {
			g.kill.Stop()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

// String returns the kept bytes, dropping a rune cut at the start.
func (t *tailBuffer) String() string { return strings.ToValidUTF8(string(t.buf), "") }
