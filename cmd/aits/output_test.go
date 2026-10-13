package main

import (
	"strings"
	"testing"
)

// TestOutputChannels enforces P5 for every kind of outcome: with --json,
// stdout is exactly one JSON object (an error envelope or a result) and stderr
// is empty; without it, stdout carries only a successful answer.
func TestOutputChannels(t *testing.T) {
	tests := []struct {
		name     string
		scenario string // "" means no agent on PATH
		args     []string
		code     int
	}{
		{"unknown flag", "ok", submitArgs("--bogus", "hi"), 2},
		{"unknown command", "ok", []string{"nope"}, 2},
		{"invalid input", "ok", []string{"shell", "submit", "hi"}, 2},
		{"agent not found", "", submitArgs("hi"), 1},
		{"agent failed", "exit_nonzero", submitArgs("hi"), 1},
		{"bad output", "not_json", submitArgs("hi"), 1},
		{"ok", "ok", submitArgs("hi"), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := func() string {
				if tt.scenario == "" {
					return ""
				}
				return vendor(t, tt.scenario)
			}
			out := call{path: path(), args: append(tt.args, "--json")}.run(t)
			r := decode(t, out.stdout)
			if out.code != tt.code || out.stderr != "" || (r.Error == nil) == (r.Status == "") {
				t.Errorf("--json: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
			}
			out = call{path: path(), args: tt.args}.run(t)
			if out.code != tt.code {
				t.Errorf("text: code=%d, want %d", out.code, tt.code)
			}
			if tt.code == 0 {
				if out.stdout != "fake answer\n" {
					t.Errorf("text: stdout=%q", out.stdout)
				}
			} else if out.stdout != "" || !strings.HasPrefix(out.stderr, "aits: ") {
				t.Errorf("text: stdout=%q stderr=%q", out.stdout, out.stderr)
			}
		})
	}
}
