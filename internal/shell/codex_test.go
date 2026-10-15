package shell

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCodex(t *testing.T) {
	const (
		started   = `{"type":"thread.started","thread_id":"t1"}`
		turn      = `{"type":"turn.started"}`
		message   = `{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"the answer"}}`
		completed = `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`
	)
	lines := func(l ...string) string { return strings.Join(l, "\n") + "\n" }
	ok := events{sessionID: "t1", answer: "the answer", done: true}
	tests := []struct {
		name string
		in   string
		want events
	}{
		{"turn", lines(started, turn, message, completed), ok},
		{"last message wins", lines(started, turn,
			`{"type":"item.completed","item":{"type":"agent_message","text":"first"}}`,
			`{"type":"item.completed","item":{"type":"command_execution","command":"ls","aggregated_output":"x","exit_code":0}}`,
			message, completed), ok},
		{"large tool output", lines(started, turn,
			`{"type":"item.completed","item":{"type":"command_execution","aggregated_output":"`+strings.Repeat("x", 1<<20)+`"}}`,
			message, completed), ok},
		{"retry notices ignored", lines(started, turn, `{"type":"error","message":"Reconnecting... 1/5"}`, message, completed), ok},
		{"unknown event types", lines(started, turn, `{"type":"turn.diff","diff":"--- a"}`, message, completed), ok},
		{"first thread id wins", lines(started, `{"type":"thread.started","thread_id":"t2"}`, turn, message, completed), ok},
		{"turn failed", lines(started, turn, `{"type":"error","message":"retry"}`, `{"type":"turn.failed","error":{"message":"401 Unauthorized"}}`),
			events{sessionID: "t1", done: true, failed: true, failure: "401 Unauthorized"}},
		{"turn failed without message", lines(started, turn, `{"type":"turn.failed"}`),
			events{sessionID: "t1", done: true, failed: true}},
		{"no turn.completed", lines(started, turn, message), events{sessionID: "t1", answer: "the answer"}},
		{"no thread id", lines(turn, message, completed), events{answer: "the answer", done: true}},
		{"not json first", lines("hello", started, turn, message, completed),
			events{sessionID: "t1", answer: "the answer", done: true, badLine: true}},
		{"json null", "null\n", events{badLine: true}},
		{"message text of wrong type", `{"type":"item.completed","item":{"type":"agent_message","text":42}}`, events{badLine: true}},
		{"thread id of wrong type", `{"type":"thread.started","thread_id":7}`, events{badLine: true}},
		{"other item with odd fields", `{"type":"item.completed","item":{"type":"reasoning","text":42}}`, events{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := readEvents(strings.NewReader(tt.in), parseCodex); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("readEvents = %+v, want %+v", got, tt.want)
			}
		})
	}
}
