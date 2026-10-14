package shell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRequestValidate(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	good := Request{Agent: "claude", Prompt: "hi", Dir: dir, Timeout: time.Minute, Environ: []string{"SET=1"}, Pass: []string{"SET"}}
	if _, err := good.validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	for _, id := range []string{"eab7bb86-4763-40f2-ae77-15cb6e443485", "01a13843-b160-7d40-9d06-02c6749fa5ed", "a.b_c-1"} {
		r := good
		r.Session = id
		if _, err := r.validate(); err != nil {
			t.Errorf("session %q rejected: %v", id, err)
		}
	}
	codex := good
	codex.Agent = "codex"
	if _, err := codex.validate(); err != nil {
		t.Fatalf("valid codex request rejected: %v", err)
	}
	tests := []struct {
		name string
		edit func(*Request)
	}{
		{"no agent", func(r *Request) { r.Agent = "" }},
		{"unknown agent", func(r *Request) { r.Agent = "nope" }},
		{"blank prompt", func(r *Request) { r.Prompt = " \n\t" }},
		{"relative dir", func(r *Request) { r.Dir = "rel" }},
		{"missing dir", func(r *Request) { r.Dir = filepath.Join(dir, "missing") }},
		{"dir is a file", func(r *Request) { r.Dir = file }},
		{"zero timeout", func(r *Request) { r.Timeout = 0 }},
		{"bad pass name", func(r *Request) { r.Pass = []string{"A=B"} }},
		{"empty pass name", func(r *Request) { r.Pass = []string{""} }},
		{"session starting with a dash", func(r *Request) { r.Session = "-x" }},
		{"session that is a flag", func(r *Request) { r.Session = "--dangerously-skip-permissions" }},
		{"session with a space", func(r *Request) { r.Session = "a b" }},
		{"session with a semicolon", func(r *Request) { r.Session = "a;b" }},
		{"blank session", func(r *Request) { r.Session = " " }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := good
			tt.edit(&r)
			_, err := r.validate()
			var invalid *InvalidRequestError
			if !errors.As(err, &invalid) {
				t.Fatalf("validate() = %v, want *InvalidRequestError", err)
			}
		})
	}
	// Whether a --env variable is set is Submit's check, not Validate's: a
	// queued task is validated before the worker's environment exists.
	r := good
	r.Pass = []string{"UNSET"}
	if err := r.Validate(); err != nil {
		t.Errorf("Validate() with an unset variable = %v", err)
	}
	var invalid *InvalidRequestError
	if _, err := Submit(context.Background(), r); !errors.As(err, &invalid) {
		t.Errorf("Submit() with an unset variable = %v, want *InvalidRequestError", err)
	}
}

// writeExe writes a shell script named name into dir with mode.
func writeExe(t *testing.T, dir, name, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), mode); err != nil {
		t.Fatal(err)
	}
}

// The agent is looked up on the PATH the agent will get: the last PATH in
// the request's environment (P8), never aits's own.
func TestLookPath(t *testing.T) {
	a, empty, c, r := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	writeExe(t, a, "claude", "", 0o755)
	writeExe(t, c, "claude", "", 0o644)
	writeExe(t, r, "claude", "", 0o755)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(wd, r)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(a, "claude")
	tests := []struct {
		name    string
		environ []string
		want    string // "" means ErrAgentNotFound
		msg     string // exact error text, when set
	}{
		{"found", []string{"PATH=" + a}, want, ""},
		{"second entry", []string{"PATH=" + empty + ":" + a}, want, ""},
		{"not executable", []string{"PATH=" + c + ":" + a}, want, ""},
		{"last PATH wins", []string{"PATH=" + empty, "PATH=" + a}, want, ""},
		{"last PATH empty", []string{"PATH=" + a, "PATH=" + empty}, "", ""},
		{"not on PATH", []string{"PATH=" + empty}, "", "agent executable not found: claude is not on PATH"},
		{"no PATH", []string{"HOME=" + empty}, "", "agent executable not found: claude is not on PATH (PATH is not set)"},
		{"relative entry first", []string{"PATH=" + rel + ":" + a}, "", ""},
		{"empty entry", []string{"PATH=:" + a}, want, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := lookPath(tt.environ, "claude")
			if tt.want != "" {
				if err != nil || got != tt.want {
					t.Fatalf("lookPath = %q, %v; want %q", got, err, tt.want)
				}
				return
			}
			if !errors.Is(err, ErrAgentNotFound) || (tt.msg != "" && err.Error() != tt.msg) {
				t.Fatalf("lookPath = %q, %v; want ErrAgentNotFound %q", got, err, tt.msg)
			}
		})
	}
}

// Submit ignores aits's own PATH: a claude there must not run.
func TestSubmitIgnoresProcessPATH(t *testing.T) {
	proc := t.TempDir()
	writeExe(t, proc, "claude", "exit 4", 0o755)
	t.Setenv("PATH", proc)
	req := Request{Agent: "claude", Prompt: "hi", Dir: t.TempDir(), Timeout: time.Minute, Environ: []string{"PATH=" + t.TempDir()}}
	if res, err := Submit(context.Background(), req); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("Submit = %+v, %v; want ErrAgentNotFound", res, err)
	}
}
