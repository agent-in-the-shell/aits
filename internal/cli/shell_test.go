package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/agent-in-the-shell/aits/internal/shell"
)

// A result without a status would read as nothing happened: it is refused,
// in both modes, before anything is printed (P6).
func TestWriteResultRefusesEmptyStatus(t *testing.T) {
	for _, json := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		_, err := writeResult(&Env{Stdout: &stdout, Stderr: &stderr, JSON: json}, shell.Result{Agent: "claude", SessionID: "s"})
		if errCode(err) != "internal" || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Errorf("json=%v: err=%v stdout=%q stderr=%q; want internal and nothing printed", json, err, stdout.String(), stderr.String())
		}
	}
}

// The help prints the exact text --report appends to the prompt (#157).
func TestReportInstructionInHelp(t *testing.T) {
	for _, line := range strings.Split(strings.TrimSpace(shell.ReportInstruction), "\n") {
		if !strings.Contains(submitDetails, "  "+line+"\n") {
			t.Errorf("submit help lacks %q", line)
		}
	}
}
