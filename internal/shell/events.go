package shell

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// maxEventSize bounds one line of agent output (P8).
const maxEventSize = 16 << 20

// events is what aits keeps of the agent's stream-json output: the first
// session id and the result event. Every other event is parsed and dropped.
type events struct {
	sessionID string
	result    *resultEvent
	notJSON   bool
	tooLarge  bool
}

// header holds the fields aits reads from every event. Decoding into it skips
// all other fields without copying them, however large they are.
type header struct {
	Type      json.RawMessage `json:"type"`
	SessionID json.RawMessage `json:"session_id"`
}

type resultEvent struct {
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
}

// readEvents reads r until EOF, a read error, or the first line longer than
// maxEventSize. It holds at most one line in memory. A line that is not JSON
// is recorded but does not stop parsing, so a later session id is still kept.
func readEvents(r io.Reader) events {
	var ev events
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxEventSize+1)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if !ev.add(line) {
			ev.notJSON = true
		}
	}
	ev.tooLarge = errors.Is(sc.Err(), bufio.ErrTooLong)
	return ev
}

// add records one event line and reports whether it was a usable JSON object.
func (ev *events) add(line []byte) bool {
	var h *header // stays nil for the JSON literal null
	if err := json.Unmarshal(line, &h); err != nil || h == nil {
		return false
	}
	if ev.sessionID == "" {
		var id string
		if json.Unmarshal(h.SessionID, &id) == nil {
			ev.sessionID = id
		}
	}
	var typ string
	if json.Unmarshal(h.Type, &typ) != nil || typ != "result" {
		return true
	}
	var res resultEvent
	if err := json.Unmarshal(line, &res); err != nil {
		return false
	}
	ev.result = &res
	return true
}
