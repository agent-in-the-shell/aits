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
