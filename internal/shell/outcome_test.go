package shell

import (
	"errors"
	"testing"
)

func TestDecide(t *testing.T) {
	okEvents := events{sessionID: "s1", answer: "answer", done: true}
	tests := []struct {
		name string
		obs  observation
		want Status
	}{
		{"ok", observation{events: okEvents}, StatusOK},
		{"aborted first", observation{aborted: errors.New("x"), stopped: true, timedOut: true, exitCode: -1}, StatusStopped},
		{"stopped first", observation{stopped: true, timedOut: true, exitCode: -1, events: events{tooLarge: true}}, StatusStopped},
		{"timeout before too large", observation{timedOut: true, exitCode: -1, events: events{tooLarge: true}}, StatusTimeout},
		{"too large before exit code", observation{exitCode: -1, events: events{tooLarge: true}}, StatusTooLarge},
		{"exit code before bad output", observation{exitCode: 3, events: events{badLine: true}}, StatusAgentFailed},
		{"bad line", observation{events: events{badLine: true}}, StatusBadOutput},
		{"bad line despite a good result", observation{events: events{sessionID: "s1", answer: "a", done: true, badLine: true}}, StatusBadOutput},
		{"reported failure", observation{events: events{sessionID: "s1", done: true, failed: true, failure: "boom"}}, StatusAgentFailed},
		{"no final event", observation{events: events{sessionID: "s1", answer: "a"}}, StatusBadOutput},
		{"empty answer", observation{events: events{sessionID: "s1", done: true}}, StatusBadOutput},
		{"no session id", observation{events: events{answer: "answer", done: true}}, StatusBadOutput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, msg := decide(tt.obs)
			if got != tt.want {
				t.Fatalf("decide = %s, want %s", got, tt.want)
			}
			if (got == StatusOK) != (msg == "") {
				t.Errorf("status %s with message %q", got, msg)
			}
		})
	}
}

// TestFailureMessage: the agent's own error beats its exit code, which hides
// the reason (Claude not logged in exits 1 with an empty stderr).
func TestFailureMessage(t *testing.T) {
	tests := []struct {
		name string
		obs  observation
		want string
	}{
		{"reported error with non-zero exit",
			observation{exitCode: 1, events: events{sessionID: "s1", done: true, failed: true, failure: "Not logged in\nmore"}},
			"the agent reported an error: Not logged in"},
		{"reported error with exit 0",
			observation{events: events{sessionID: "s1", done: true, failed: true, failure: "boom"}},
			"the agent reported an error: boom"},
		{"exit code only", observation{exitCode: 3}, "the agent exited with code 3"},
		{"reported error without text", observation{events: events{done: true, failed: true}}, "the agent reported an error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if status, got := decide(tt.obs); status != StatusAgentFailed || got != tt.want {
				t.Errorf("decide = %s, %q; want agent_failed, %q", status, got, tt.want)
			}
		})
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"one\ntwo", 10, "one"},
		{"abcdef", 3, "abc…"},
		{"héllo", 2, "h…"},
	}
	for _, tt := range tests {
		if got := firstLine(tt.in, tt.max); got != tt.want {
			t.Errorf("firstLine(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
		}
	}
}
