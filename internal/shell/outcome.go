package shell

import (
	"fmt"
	"strings"
)

// observation is everything aits saw of one run.
type observation struct {
	aborted    error // Request.Started failed, and the agent was killed
	stopped    bool
	timedOut   bool
	exitCode   int
	events     events
	stderrTail string
}

// decide maps an observation to a status and a one-line message, checking the
// conditions in the order of the #2 spec §6.
func decide(o observation) (Status, string) {
	ev := o.events
	switch {
	case o.aborted != nil:
		return StatusStopped, o.aborted.Error()
	case o.stopped:
		return StatusStopped, "aits was stopped by a signal before the agent finished"
	case o.timedOut:
		return StatusTimeout, "the agent did not finish before the timeout"
	case ev.tooLarge:
		return StatusTooLarge, fmt.Sprintf("an agent output event exceeded %d MiB", maxEventSize>>20)
	case o.exitCode != 0:
		return StatusAgentFailed, failureMessage(ev, o.exitCode)
	case ev.badLine:
		return StatusBadOutput, "the agent printed a line that is not a usable JSON event"
	case ev.failed:
		return StatusAgentFailed, failureMessage(ev, o.exitCode)
	case !ev.done:
		return StatusBadOutput, "the agent output has no final event"
	case ev.answer == "":
		return StatusBadOutput, "the agent's answer is empty"
	case ev.sessionID == "":
		return StatusBadOutput, "the agent output has no session id"
	}
	return StatusOK, ""
}

// failureMessage prefers the error the agent reported over its exit code,
// which alone hides the reason.
func failureMessage(ev events, exitCode int) string {
	switch {
	case ev.failure != "":
		return "the agent reported an error: " + firstLine(ev.failure, 200)
	case exitCode != 0:
		return fmt.Sprintf("the agent exited with code %d", exitCode)
	}
	return "the agent reported an error"
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
