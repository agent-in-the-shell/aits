// Package cli implements the aits command line: the shared grammar (P4), the
// command tree and help, and the output and exit-code rules (P5).
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// Exit codes (P5).
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// Env is the process environment a command runs in.
type Env struct {
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Environ []string        // KEY=VALUE entries, as os.Environ returns them
	JSON    bool            // set from --json before the command runs
	Kill    <-chan struct{} // closed on a second stop signal: see shell.Request.Kill
}

// Error is a failure reported to the caller under a stable code (P5).
type Error struct {
	Code    string
	Message string
	exit    int
}

func (e *Error) Error() string { return e.Message }

func usageError(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...), exit: exitUsage}
}

func failure(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...), exit: exitFailure}
}

// Run executes one command line (without the program name) and returns the
// process exit code.
func Run(ctx context.Context, args []string, env Env) int {
	env.JSON = wantsJSON(args)
	code, err := dispatch(ctx, Root(), args, &env)
	if err != nil {
		return report(&env, err)
	}
	return code
}

// report writes err to the channel P5 assigns and returns its exit code.
func report(env *Env, err error) int {
	var e *Error
	if !errors.As(err, &e) {
		e = failure("internal", "%v", err)
	}
	if env.JSON {
		writeJSON(env.Stdout, map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
	} else {
		fmt.Fprintf(env.Stderr, "aits: %s\n", e.Message)
	}
	return e.exit
}

func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// wantsJSON reports whether --json is set before "--". It picks the error
// channel for failures that happen before the flags are fully parsed.
func wantsJSON(args []string) bool {
	on := false
	for _, a := range args {
		if a == "--" {
			break
		}
		name, value, hasValue, ok := flagName(a)
		if !ok || name != "json" {
			continue
		}
		if !hasValue {
			on = true
		} else if b, err := strconv.ParseBool(value); err == nil {
			on = b
		}
	}
	return on
}
