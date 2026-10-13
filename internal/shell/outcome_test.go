package shell

import "testing"

func TestDecide(t *testing.T) {
	okEvents := events{sessionID: "s1", result: &resultEvent{Result: "answer", SessionID: "s1"}}
	tests := []struct {
		name string
		obs  observation
		want Status
	}{
		{"ok", observation{events: okEvents}, StatusOK},
		{"stopped first", observation{stopped: true, timedOut: true, exitCode: -1, events: events{tooLarge: true}}, StatusStopped},
		{"timeout before too large", observation{timedOut: true, exitCode: -1, events: events{tooLarge: true}}, StatusTimeout},
		{"too large before exit code", observation{exitCode: -1, events: events{tooLarge: true}}, StatusTooLarge},
		{"exit code before bad output", observation{exitCode: 3, events: events{notJSON: true}}, StatusAgentFailed},
		{"not json", observation{events: events{notJSON: true}}, StatusBadOutput},
		{"no result", observation{events: events{sessionID: "s1"}}, StatusBadOutput},
		{"error result", observation{events: events{sessionID: "s1", result: &resultEvent{IsError: true, Result: "boom"}}}, StatusAgentFailed},
		{"empty result", observation{events: events{sessionID: "s1", result: &resultEvent{}}}, StatusBadOutput},
		{"no session id", observation{events: events{result: &resultEvent{Result: "answer"}}}, StatusBadOutput},
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
