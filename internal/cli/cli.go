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
	Self    string          // the path aits was started as: os.Args[0]
}

// Error is a failure reported to the caller under a stable code (P5).
type Error struct {
	Code    string
	Message string
	exit    int
}

func (e *Error) Error() string { return e.Message }

// UsageError is an error with code that exits 2 (P5): bad usage or invalid
// input.
func UsageError(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...), exit: exitUsage}
}

// Failure is an error with code that exits 1 (P5): the operation failed.
func Failure(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...), exit: exitFailure}
}

// Run executes one command line (without the program name) of the command
// tree root, Root() for aits or another tree built with NewRoot, and returns
// the process exit code: one grammar (P4) and output rules (P5) for both
// binaries.
func Run(ctx context.Context, root *Command, args []string, env Env) int {
	env.JSON = wantsJSON(args)
	code, err := dispatch(ctx, root, args, &env)
	if err != nil {
		return report(&env, root.Name, err)
	}
	return code
}

// report writes err to the channel P5 assigns, prefixed with the program
// name on stderr, and returns its exit code.
func report(env *Env, program string, err error) int {
	var e *Error
	if !errors.As(err, &e) {
		e = Failure("internal", "%v", err)
	}
	if env.JSON {
		WriteJSON(env.Stdout, map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
	} else {
		fmt.Fprintf(env.Stderr, "%s: %s\n", program, e.Message)
	}
	return e.exit
}

// WriteJSON writes v as one line of JSON, without HTML escaping.
func WriteJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// wantsJSON reports whether the last --json before "--" is true. It selects
// JSON for the error and the result, even when the line does not parse.
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
