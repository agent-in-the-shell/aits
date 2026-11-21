// Package gw is aits-gw, the event gateway that P15(4) allows as a second
// binary (#163). Nothing aits builds imports it (P1). Stage 0 is shadow mode
// (#165): it records events and triggers or delivers nothing.
//
// Events reach it through a spool directory, $AITS_HOME/gw/spool: "event put"
// writes one file there and needs no running server (P10); "serve" moves the
// spool into a SQLite database, recording each (kind, source_id) once.
package gw

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"time"
	"unicode"
)

// Limits on an event, so one bad caller cannot fill the spool.
const (
	maxName    = 128       // KIND and payload keys
	maxSource  = 256       // --source-id
	maxPayload = 64 * 1024 // the encoded event
)

// Event is one spooled event: the file "event put" writes and the row
// "serve" records.
type Event struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	SourceID string            `json:"source_id"`
	Payload  map[string]string `json:"payload"`
	PutAt    string            `json:"put_at"`
}

// newID returns an event id that sorts by the time it was made, so the spool
// is read in the order events were put.
func newID(now time.Time) string {
	return fmt.Sprintf("%s-%016x", now.UTC().Format("20060102T150405.000000000Z"), rand.Uint64())
}

// validName reports why s is not a valid KIND or payload key, or "": lowercase
// letters, digits, '.', '_' and '-', starting with a letter or digit.
func validName(s string) string {
	if s == "" {
		return "is empty"
	}
	if len(s) > maxName {
		return fmt.Sprintf("is longer than %d bytes", maxName)
	}
	for i, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || i > 0 && (r == '.' || r == '_' || r == '-')
		if !ok {
			return "may hold only lowercase letters, digits, '.', '_' and '-', starting with a letter or digit"
		}
	}
	return ""
}

// validSource reports why s is not a valid source id, or "".
func validSource(s string) string {
	if s == "" {
		return "is empty"
	}
	if len(s) > maxSource {
		return fmt.Sprintf("is longer than %d bytes", maxSource)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "holds a control character"
		}
	}
	return ""
}

// check returns an error naming the first invalid field of e, or nil.
func (e *Event) check() error {
	if e.ID == "" || e.PutAt == "" || e.Payload == nil {
		return fmt.Errorf("event is missing id, put_at or payload")
	}
	if why := validName(e.Kind); why != "" {
		return fmt.Errorf("KIND %q %s", e.Kind, why)
	}
	if why := validSource(e.SourceID); why != "" {
		return fmt.Errorf("--source-id %s", why)
	}
	for k := range e.Payload {
		if why := validName(k); why != "" {
			return fmt.Errorf("payload key %q %s", k, why)
		}
	}
	return nil
}

// parseEvent decodes and checks one spool file.
func parseEvent(b []byte) (*Event, error) {
	if len(b) > maxPayload {
		return nil, errTooLarge
	}
	var e Event
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, err
	}
	if err := e.check(); err != nil {
		return nil, err
	}
	return &e, nil
}
