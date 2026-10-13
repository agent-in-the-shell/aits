package shell

import (
	"fmt"
	"strings"
)

// observation is everything aits saw of one run.
type observation struct {
	stopped    bool
	timedOut   bool
	exitCode   int
	events     events
	stderrTail string
}

// decide maps an observation to a status and a one-line message, checking the
// conditions in the order of spec §6.
func decide(o observation) (Status, string) {
	ev := o.events
	switch {
	case o.stopped:
		return StatusStopped, "aits was stopped by a signal before the agent finished"
	case o.timedOut:
		return StatusTimeout, "the agent did not finish before the timeout"
	case ev.tooLarge:
		return StatusTooLarge, fmt.Sprintf("an agent output event exceeded %d MiB", maxEventSize>>20)
	case o.exitCode != 0:
		return StatusAgentFailed, fmt.Sprintf("the agent exited with code %d", o.exitCode)
	case ev.notJSON:
		return StatusBadOutput, "the agent printed a line that is not a JSON object"
	case ev.result == nil:
		return StatusBadOutput, "the agent output has no result event"
	case ev.result.IsError:
		return StatusAgentFailed, "the agent reported an error: " + firstLine(ev.result.Result, 200)
	case ev.result.Result == "":
		return StatusBadOutput, "the agent result is empty"
	case ev.sessionID == "":
		return StatusBadOutput, "the agent output has no session id"
	}
	return StatusOK, ""
}

// firstLine returns the first line of s, cut to at most max bytes on a rune
// boundary, with an ellipsis when cut.
func firstLine(s string, max int) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "") + "…"
}
