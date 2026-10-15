package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/agent-in-the-shell/aits/internal/shell"
)

// A result without a status would read as nothing happened: it is refused,
// in both modes, before anything is printed (P6).
func TestWriteResultRefusesEmptyStatus(t *testing.T) {
	for _, json := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		_, err := writeResult(&Env{Stdout: &stdout, Stderr: &stderr, JSON: json}, shell.Result{Agent: "claude", SessionID: "s"})
		var e *Error
		if !errors.As(err, &e) || e.Code != "internal" || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Errorf("json=%v: err=%v stdout=%q stderr=%q; want internal and nothing printed", json, err, stdout.String(), stderr.String())
		}
	}
}
