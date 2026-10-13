// Package shell runs one prompt through a vendor agent CLI and reports how the
// run ended. It is the Go API other command groups call (P1); it knows nothing
// about flags or output formats.
package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DefaultTimeout bounds a run when the caller sets no other timeout.
const DefaultTimeout = 30 * time.Minute

// Status says how a run that started ended (P6: always explicit).
type Status string

const (
	StatusOK          Status = "ok"
	StatusAgentFailed Status = "agent_failed"
	StatusBadOutput   Status = "bad_agent_output"
	StatusTooLarge    Status = "output_too_large"
	StatusTimeout     Status = "timeout"
	StatusStopped     Status = "stopped"
)

// Request is one prompt to run.
type Request struct {
	Agent   string        // one of Agents()
	Prompt  string        // sent on the agent's stdin, never in argv (P8)
	Dir     string        // absolute working directory
	Timeout time.Duration // must be positive
	Environ []string      // the caller's environment, KEY=VALUE
	Pass    []string      // variable names passed from Environ beyond the allowlist (P8)
}

// Result describes a run that started.
type Result struct {
	Status     Status `json:"status"`
	Agent      string `json:"agent"`
	SessionID  string `json:"session_id,omitempty"`
	Answer     string `json:"answer,omitempty"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	StderrTail string `json:"stderr_tail,omitempty"`
	Message    string `json:"message,omitempty"`
}

// ErrAgentNotFound means the agent's executable is not on PATH.
var ErrAgentNotFound = errors.New("agent executable not found")

// InvalidRequestError means the request was rejected before anything started.
type InvalidRequestError struct{ Reason string }

func (e *InvalidRequestError) Error() string { return "invalid request: " + e.Reason }

func invalid(format string, a ...any) error {
	return &InvalidRequestError{Reason: fmt.Sprintf(format, a...)}
}

func (r Request) validate() (agent, error) {
	choices := strings.Join(Agents(), ", ")
	if r.Agent == "" {
		return agent{}, invalid("agent is required (one of: %s)", choices)
	}
	ag, ok := agents[r.Agent]
	if !ok {
		return agent{}, invalid("unknown agent %q (one of: %s)", r.Agent, choices)
	}
	if strings.TrimSpace(r.Prompt) == "" {
		return agent{}, invalid("prompt is empty")
	}
	if !filepath.IsAbs(r.Dir) {
		return agent{}, invalid("working directory %q is not absolute", r.Dir)
	}
	if fi, err := os.Stat(r.Dir); err != nil || !fi.IsDir() {
		return agent{}, invalid("working directory %q is not a directory", r.Dir)
	}
	if r.Timeout <= 0 {
		return agent{}, invalid("timeout must be positive, got %s", r.Timeout)
	}
	for _, name := range r.Pass {
		if name == "" || strings.Contains(name, "=") {
			return agent{}, invalid("%q is not an environment variable name", name)
		}
		if _, ok := lookup(r.Environ, name); !ok {
			return agent{}, invalid("environment variable %s is not set", name)
		}
	}
	return ag, nil
}

// Submit validates req, runs the agent, and waits until it finishes, times
// out, or is stopped through ctx. A non-nil error means nothing was started.
func Submit(ctx context.Context, req Request) (Result, error) {
	ag, err := req.validate()
	if err != nil {
		return Result{}, err
	}
	path, err := exec.LookPath(ag.executable)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %s is not on PATH", ErrAgentNotFound, ag.executable)
	}
	start := time.Now()
	obs, err := run(ctx, ag, path, req)
	if err != nil {
		return Result{}, err
	}
	status, msg := decide(obs)
	res := Result{
		Status:     status,
		Agent:      ag.name,
		SessionID:  obs.events.sessionID,
		ExitCode:   obs.exitCode,
		DurationMS: time.Since(start).Milliseconds(),
		Message:    msg,
	}
	switch status {
	case StatusOK:
		res.Answer = obs.events.result.Result
	case StatusAgentFailed:
		res.StderrTail = obs.stderrTail
	}
	return res, nil
}
