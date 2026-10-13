package shell

import (
	"reflect"
	"strings"
	"testing"
)

func TestReadEvents(t *testing.T) {
	const initLine = `{"type":"system","subtype":"init","session_id":"s1"}`
	const resultLine = `{"type":"result","is_error":false,"result":"the answer","session_id":"s1"}`
	okResult := &resultEvent{Result: "the answer", SessionID: "s1"}
	tests := []struct {
		name string
		in   string
		want events
	}{
		{"stream", initLine + "\n" + `{"type":"assistant","session_id":"s1","message":{}}` + "\n" + resultLine + "\n",
			events{sessionID: "s1", result: okResult}},
		{"first session id wins", initLine + "\n" + `{"type":"result","result":"a","session_id":"s2"}` + "\n",
			events{sessionID: "s1", result: &resultEvent{Result: "a", SessionID: "s2"}}},
		{"session id only on result", `{"type":"result","result":"a","session_id":"s2"}`,
			events{sessionID: "s2", result: &resultEvent{Result: "a", SessionID: "s2"}}},
		{"blank lines ignored", "\n\n" + resultLine + "\n\n", events{sessionID: "s1", result: okResult}},
		{"error result", `{"type":"result","is_error":true,"result":"bad","session_id":"s1"}`,
			events{sessionID: "s1", result: &resultEvent{IsError: true, Result: "bad", SessionID: "s1"}}},
		{"not json", "hello\n" + resultLine, events{notJSON: true}},
		{"json array", "[1,2]\n", events{notJSON: true}},
		{"json null", "null\n", events{notJSON: true}},
		{"result of wrong type", `{"type":"result","result":42}`, events{notJSON: true}},
		{"no result event", initLine, events{sessionID: "s1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := readEvents(strings.NewReader(tt.in)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("readEvents = %+v (result %+v), want %+v (result %+v)", got, got.result, tt.want, tt.want.result)
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
	if ev := readEvents(strings.NewReader(line(maxEventSize) + "\n")); ev.tooLarge || ev.notJSON {
		t.Errorf("a %d-byte event was rejected: tooLarge=%v notJSON=%v", maxEventSize, ev.tooLarge, ev.notJSON)
	}
	if ev := readEvents(strings.NewReader(line(maxEventSize+1) + "\n")); !ev.tooLarge {
		t.Errorf("a %d-byte event was accepted", maxEventSize+1)
	}
}
