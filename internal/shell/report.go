package shell

import (
	"encoding/json"
	"slices"
	"strings"
)

// ReportInstruction is what Request.Report appends to the prompt (#157):
// fixed, and printed in help, so nothing reaches the agent unseen.
const ReportInstruction = `

When you finish, end your reply with one last line of exactly this form,
with nothing after it:
AITS-REPORT {"state":"done","summary":"one sentence on what you did"}
Use "state":"blocked" with a "question" field when you need a decision or
information to go on, and "state":"failed" when you could not do the task.`

// reportPrefix starts the line the agent ends a reported run with.
const reportPrefix = "AITS-REPORT "

// reportMax bounds the summary and the question kept (P8).
const reportMax = 2000

// Report is the agent's typed account of a run (#157).
type Report struct {
	State    string `json:"state"` // done, blocked or failed
	Summary  string `json:"summary"`
	Question string `json:"question,omitempty"`
}

// parseReport reads the report on the last non-empty line of answer, or
// says why there is none.
func parseReport(answer string) (*Report, string) {
	lines := strings.Split(strings.TrimRight(answer, " \t\r\n"), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	body, ok := strings.CutPrefix(last, reportPrefix)
	if !ok {
		return nil, "the answer does not end with an AITS-REPORT line"
	}
	var r Report
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return nil, "the AITS-REPORT line is not a JSON object: " + firstLine(err.Error(), 200)
	}
	if !slices.Contains([]string{"done", "blocked", "failed"}, r.State) {
		return nil, "the AITS-REPORT state " + firstLine(r.State, 50) + " is not done, blocked or failed"
	}
	r.Summary, r.Question = cut(r.Summary, reportMax), cut(r.Question, reportMax)
	return &r, ""
}
