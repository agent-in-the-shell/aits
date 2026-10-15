package shell

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseClaude(t *testing.T) {
	const initLine = `{"type":"system","subtype":"init","session_id":"s1"}`
	const resultLine = `{"type":"result","is_error":false,"result":"the answer","session_id":"s1"}`
	tests := []struct {
		name string
		in   string
		want events
	}{
		{"stream", initLine + "\n" + `{"type":"assistant","session_id":"s1","message":{}}` + "\n" + resultLine + "\n",
			events{sessionID: "s1", answer: "the answer", done: true}},
		{"first session id wins", initLine + "\n" + `{"type":"result","result":"a","session_id":"s2"}` + "\n",
			events{sessionID: "s1", answer: "a", done: true}},
		{"session id only on result", `{"type":"result","result":"a","session_id":"s2"}`,
			events{sessionID: "s2", answer: "a", done: true}},
		{"blank lines ignored", "\n\n" + resultLine + "\n\n", events{sessionID: "s1", answer: "the answer", done: true}},
		{"error result", `{"type":"result","is_error":true,"result":"bad","session_id":"s1"}`,
			events{sessionID: "s1", failure: "bad", done: true, failed: true}},
		{"not json", "hello\n" + resultLine, events{sessionID: "s1", answer: "the answer", done: true, badLine: true}},
		{"error reported in errors", `{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"s1","errors":["No conversation found","second"]}`,
			events{sessionID: "s1", failure: "No conversation found; second", done: true, failed: true}},
		{"errors of another shape are ignored", `{"type":"result","is_error":true,"session_id":"s1","errors":[{"code":1}]}`,
			events{sessionID: "s1", done: true, failed: true}},
		{"session id after a bad line", "hello\n" + initLine, events{sessionID: "s1", badLine: true}},
		{"json array", "[1,2]\n", events{badLine: true}},
		{"json null", "null\n", events{badLine: true}},
		{"result of wrong type", `{"type":"result","result":42}`, events{badLine: true}},
		{"no result event", initLine, events{sessionID: "s1"}},
		{"denials", `{"type":"result","result":"a","session_id":"s1","permission_denials":[{"tool_name":"Write","tool_input":{}},{"tool_name":"Bash"}]}`,
			events{sessionID: "s1", answer: "a", done: true, denied: []string{"Write", "Bash"}}},
		{"denials on an error", `{"type":"result","is_error":true,"result":"bad","session_id":"s1","permission_denials":[{"tool_name":"Write"}]}`,
			events{sessionID: "s1", failure: "bad", done: true, failed: true, denied: []string{"Write"}}},
		{"no denials", `{"type":"result","result":"a","session_id":"s1","permission_denials":[]}`,
			events{sessionID: "s1", answer: "a", done: true}},
		{"null denials", `{"type":"result","result":"a","session_id":"s1","permission_denials":null}`,
			events{sessionID: "s1", answer: "a", done: true}},
		{"denial without a tool", `{"type":"result","result":"a","session_id":"s1","permission_denials":[{"tool_use_id":"x"}]}`,
			events{sessionID: "s1", badLine: true}},
		{"denials of wrong type", `{"type":"result","result":"a","session_id":"s1","permission_denials":"Write"}`,
			events{sessionID: "s1", badLine: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := readEvents(strings.NewReader(tt.in), parseClaude); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("readEvents = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReadEventsSizeLimit(t *testing.T) {
	// line returns a JSON object exactly n bytes long.
	line := func(n int) string {
		const head, tail = `{"type":"assistant","pad":"`, `"}`
		return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
	}
	if ev := readEvents(strings.NewReader(line(maxEventSize)+"\n"), parseClaude); ev.tooLarge || ev.badLine {
		t.Errorf("a %d-byte event was rejected: tooLarge=%v badLine=%v", maxEventSize, ev.tooLarge, ev.badLine)
	}
	if ev := readEvents(strings.NewReader(line(maxEventSize+1)+"\n"), parseClaude); !ev.tooLarge {
		t.Errorf("a %d-byte event was accepted", maxEventSize+1)
	}
}
