package shell

import (
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
		{"not json", "hello\n" + resultLine, events{sessionID: "s1", answer: "the answer", done: true, notJSON: true}},
		{"session id after a bad line", "hello\n" + initLine, events{sessionID: "s1", notJSON: true}},
		{"json array", "[1,2]\n", events{notJSON: true}},
		{"json null", "null\n", events{notJSON: true}},
		{"result of wrong type", `{"type":"result","result":42}`, events{notJSON: true}},
		{"no result event", initLine, events{sessionID: "s1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := readEvents(strings.NewReader(tt.in), parseClaude); got != tt.want {
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
	if ev := readEvents(strings.NewReader(line(maxEventSize)+"\n"), parseClaude); ev.tooLarge || ev.notJSON {
		t.Errorf("a %d-byte event was rejected: tooLarge=%v notJSON=%v", maxEventSize, ev.tooLarge, ev.notJSON)
	}
	if ev := readEvents(strings.NewReader(line(maxEventSize+1)+"\n"), parseClaude); !ev.tooLarge {
		t.Errorf("a %d-byte event was accepted", maxEventSize+1)
	}
}
