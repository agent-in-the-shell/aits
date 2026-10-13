package shell

import (
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
		{"unset pass variable", func(r *Request) { r.Pass = []string{"UNSET"} }},
		{"bad pass name", func(r *Request) { r.Pass = []string{"A=B"} }},
		{"empty pass name", func(r *Request) { r.Pass = []string{""} }},
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
}
