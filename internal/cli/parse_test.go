package cli

import (
	"errors"
	"slices"
	"testing"
)

// errCode names the outcome of a parse for table comparison.
func errCode(err error) string {
	var e *Error
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errHelp):
		return "help"
	case errors.As(err, &e):
		return e.Code
	}
	return "other: " + err.Error()
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantPos   []string
		wantAgent string
		wantJSON  bool
		wantCode  string
	}{
		{"flags first", []string{"--agent", "claude", "--json", "hi"}, []string{"hi"}, "claude", true, ""},
		{"flags last", []string{"hi", "--agent", "claude", "--json"}, []string{"hi"}, "claude", true, ""},
		{"equals form", []string{"--agent=claude", "hi"}, []string{"hi"}, "claude", false, ""},
		{"single dash", []string{"-agent", "claude", "hi"}, []string{"hi"}, "claude", false, ""},
		{"lone dash is positional", []string{"--agent", "claude", "-"}, []string{"-"}, "claude", false, ""},
		{"double dash ends flags", []string{"--agent", "claude", "--", "--json", "-x"}, []string{"--json", "-x"}, "claude", false, ""},
		{"value may start with a dash", []string{"--agent", "--", "hi"}, []string{"hi"}, "--", false, ""},
		{"unknown flag", []string{"hi", "--nope"}, nil, "", false, "unknown_flag"},
		{"missing value", []string{"hi", "--agent"}, nil, "", false, "invalid_input"},
		{"bad bool value", []string{"--json=maybe"}, nil, "", false, "invalid_input"},
		{"help", []string{"hi", "--help"}, nil, "", false, "help"},
		{"short help", []string{"-h"}, nil, "", false, "help"},
		// #85: the whole line is checked before help is printed.
		{"unknown flag after help", []string{"--help", "--nope"}, nil, "", false, "unknown_flag"},
		{"bad value after help", []string{"--help", "--json=maybe"}, nil, "", false, "invalid_input"},
		// #85: a flag that takes one value may be given once; a boolean and a
		// repeatable flag may repeat.
		{"value flag twice", []string{"--agent", "claude", "--agent", "codex", "hi"}, nil, "", false, "invalid_input"},
		{"value flag twice, equals form", []string{"--agent=claude", "hi", "-agent=codex"}, nil, "", false, "invalid_input"},
		{"boolean twice", []string{"--json", "hi", "--json"}, []string{"hi"}, "", true, ""},
		{"repeatable twice", []string{"--env", "A", "--env", "B", "hi"}, []string{"hi"}, "", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := newFlagSet("test")
			agent := fs.String("agent", "", "")
			jsonOut := fs.Bool("json", false, "")
			repeated(fs, "env", "")
			pos, err := parseArgs(fs, tt.args)
			if got := errCode(err); got != tt.wantCode {
				t.Fatalf("error = %q (%v), want %q", got, err, tt.wantCode)
			}
			if err != nil {
				return
			}
			if !slices.Equal(pos, tt.wantPos) || *agent != tt.wantAgent || *jsonOut != tt.wantJSON {
				t.Errorf("got pos=%q agent=%q json=%v, want pos=%q agent=%q json=%v",
					pos, *agent, *jsonOut, tt.wantPos, tt.wantAgent, tt.wantJSON)
			}
		})
	}
}

func TestWantsJSON(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"--json"}, true},
		{[]string{"x", "-json"}, true},
		{[]string{"--json=false"}, false},
		{[]string{"--json", "--json=false"}, false},
		{[]string{"--", "--json"}, false},
		{[]string{"--jsonx"}, false},
		{[]string{"json"}, false},
	}
	for _, tt := range tests {
		if got := wantsJSON(tt.args); got != tt.want {
			t.Errorf("wantsJSON(%q) = %v, want %v", tt.args, got, tt.want)
		}
	}
}
