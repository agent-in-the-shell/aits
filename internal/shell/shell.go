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
	Agent   string         // one of Agents()
	Prompt  string         // sent on the agent's stdin, never in argv (P8)
	Dir     string         // absolute working directory
	Timeout time.Duration  // must be positive
	Environ []string       // the caller's environment, KEY=VALUE; its PATH also finds the agent
	Pass    []string       // variable names passed from Environ beyond the allowlist (P8)
	Session string         // a session to continue; empty starts a new one
	Started func(pgid int) // if set, called once with the agent's process group right after it starts
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

// ErrAgentNotFound means the agent's executable is not on the PATH in
// Request.Environ.
var ErrAgentNotFound = errors.New("agent executable not found")

// ErrStopped means ctx was done before the agent was started (P6).
var ErrStopped = errors.New("stopped before the agent was started")

// InvalidRequestError means the request was rejected before anything started.
type InvalidRequestError struct{ Reason string }

func (e *InvalidRequestError) Error() string { return "invalid request: " + e.Reason }

func invalid(format string, a ...any) error {
	return &InvalidRequestError{Reason: fmt.Sprintf(format, a...)}
}

// Validate checks req without looking at the environment it will run in:
// whether the --env variables are set is checked by Submit.
func (r Request) Validate() error {
	_, err := r.validate()
	return err
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
	}
	if r.Session != "" && !validSession(r.Session) {
		return agent{}, invalid("session id %q must be letters, digits, '-', '_' or '.', and not start with '-'", r.Session)
	}
	return ag, nil
}

// validSession reports whether id is safe to place in the agent's argv: it
// cannot be read as a flag or split by anything (P6).
func validSession(id string) bool {
	if strings.HasPrefix(id, "-") {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.", c)) {
			return false
		}
	}
	return true
}

// Submit validates req, runs the agent, and waits until it finishes, times
// out, or is stopped through ctx. A non-nil error means nothing was started.
func Submit(ctx context.Context, req Request) (Result, error) {
	ag, err := req.validate()
	if err != nil {
		return Result{}, err
	}
	for _, name := range req.Pass {
		if _, ok := lookupEnv(req.Environ, name); !ok {
			return Result{}, invalid("environment variable %s is not set", name)
		}
	}
	env := ag.environ(req.Environ, req.Pass)
	path, err := lookPath(env, ag.executable)
	if err != nil {
		return Result{}, err
	}
	if ctx.Err() != nil {
		return Result{}, ErrStopped
	}
	start := time.Now()
	obs, err := run(ctx, ag, path, env, req)
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
		res.Answer = obs.events.answer
	case StatusAgentFailed:
		res.StderrTail = obs.stderrTail
	}
	return res, nil
}

// lookPath finds name on the PATH in env, the agent's own environment (P8),
// never on aits's. A first match in an empty or relative entry is refused, as
// exec.LookPath does: it would resolve against aits's working directory, not
// the agent's (P6).
func lookPath(env []string, name string) (string, error) {
	path, ok := lookupEnv(env, "PATH")
	if !ok {
		return "", fmt.Errorf("%w: %s is not on PATH (PATH is not set)", ErrAgentNotFound, name)
	}
	for _, dir := range filepath.SplitList(path) {
		file, abs := filepath.Join(dir, name), filepath.IsAbs(dir)
		if !abs {
			file = "." + string(filepath.Separator) + file // never a bare name: that searches aits's PATH
		}
		if _, err := exec.LookPath(file); err == nil {
			if abs {
				return file, nil
			}
			break
		}
	}
	return "", fmt.Errorf("%w: %s is not on PATH", ErrAgentNotFound, name)
}
