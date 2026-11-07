package task

import (
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/agent-in-the-shell/aits/internal/shell"
	"github.com/agent-in-the-shell/aits/internal/store"
)

// The task event log (#147): one JSON line per transition, for a caller that
// follows the queue (tail -F) instead of listing it again. It is a hint: the
// task files stay the record, and a line that could not be written is lost
// without failing the command that made the transition.
const (
	eventsName = "events.jsonl"
	// eventsMax is the size at which Create rotates the log; eventsKeep
	// rotated logs are kept (P8: bounded output).
	eventsMax  = 4 << 20
	eventsKeep = 3
)

// Event kinds.
const (
	EventCreated   = "created"
	EventClaimed   = "claimed"
	EventStarted   = "started"
	EventEnded     = "ended"
	EventCancelled = "cancelled"
)

// Event is one line of the event log.
type Event struct {
	At        time.Time    `json:"at"`
	ID        string       `json:"id"`
	Event     string       `json:"event"`
	Status    Status       `json:"status"`
	RunStatus shell.Status `json:"run_status,omitempty"`
	Label     string       `json:"label,omitempty"`
}

func eventsPath(dir string) string { return filepath.Join(dir, eventsName) }

// emit appends the event kind of t to the log. One write per line, appended,
// so lines from concurrent writers do not interleave.
func emit(dir, kind string, t Task) {
	ev := Event{At: time.Now().UTC(), ID: t.ID, Event: kind, Status: t.Status, Label: t.Label}
	if t.Run != nil {
		ev.RunStatus = t.Run.Status
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	f, err := store.OpenAppend(eventsPath(dir))
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	f.Close()
}
