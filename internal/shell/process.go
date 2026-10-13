package shell

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// stderrTailSize is how much of the agent's stderr is kept for agent_failed.
const stderrTailSize = 4 << 10

// run starts the agent and waits for it to exit.
func run(ctx context.Context, ag agent, path string, req Request) (observation, error) {
	cmd := exec.Command(path, ag.args...)
	cmd.Dir = req.Dir
	cmd.Env = ag.environ(req.Environ, req.Pass)
	cmd.Stdin = strings.NewReader(req.Prompt)
	tail := &tailBuffer{max: stderrTailSize}
	cmd.Stderr = tail
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return observation{}, err
	}
	if err := cmd.Start(); err != nil {
		return observation{}, fmt.Errorf("start %s: %w", ag.name, err)
	}
	ev := readEvents(stdout)
	_ = cmd.Wait()
	return observation{exitCode: cmd.ProcessState.ExitCode(), events: ev, stderrTail: tail.String()}, nil
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
