package shell

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// maxEventSize bounds one line of agent output (P8).
const maxEventSize = 16 << 20

// events is what aits keeps of an agent's JSON event stream, in a form that
// does not depend on the vendor. Each agent's parser fills it; every event is
// dropped once parsed.
type events struct {
	sessionID string // the first session id seen
	answer    string // the reply text
	failure   string // the error the agent reported
	done      bool   // the agent's final event was seen
	failed    bool   // the agent reported that the run failed
	notJSON   bool   // a line was not a usable event
	tooLarge  bool   // a line exceeded maxEventSize
}

// setSession records id unless a session id was already seen.
func (ev *events) setSession(id string) {
	if ev.sessionID == "" {
		ev.sessionID = id
	}
}

// parser records one event line in ev and reports whether it was a usable
// event of the agent's format.
type parser func(ev *events, line []byte) bool

// readEvents reads r until EOF, a read error, or the first line longer than
// maxEventSize. It holds at most one line in memory. A line that is not JSON
// is recorded but does not stop parsing, so a later session id is still kept.
func readEvents(r io.Reader, parse parser) events {
	var ev events
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxEventSize+1)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if !parse(&ev, line) {
			ev.notJSON = true
		}
	}
	ev.tooLarge = errors.Is(sc.Err(), bufio.ErrTooLong)
	return ev
}
