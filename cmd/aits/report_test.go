package main

import (
	"strings"
	"testing"
)

type reportResult struct {
	submitResult
	Report *struct {
		State    string `json:"state"`
		Summary  string `json:"summary"`
		Question string `json:"question"`
	} `json:"report"`
	ReportError string `json:"report_error"`
}

// --report appends the instruction to the prompt and reads the answer's
// last line; without it, nothing changes (#157).
func TestSubmitReport(t *testing.T) {
	dir := vendor(t, "report_blocked")
	out := call{path: dir, args: submitArgs("--json", "--report", "do it")}.run(t)
	r := decodeOne[reportResult](t, out.stdout)
	if out.code != 0 || r.Status != "ok" || r.Report == nil || r.Report.State != "blocked" || r.Report.Question != "Which API key?" || r.ReportError != "" {
		t.Fatalf("code=%d stdout=%s", out.code, out.stdout)
	}
	if p := readRecord(t, dir).Stdin; !strings.HasPrefix(p, "do it\n\nWhen you finish") || !strings.HasSuffix(p, "could not do the task.") {
		t.Errorf("prompt sent = %q", p)
	}

	out = call{path: dir, args: submitArgs("--json", "do it")}.run(t)
	if r := decodeOne[reportResult](t, out.stdout); r.Report != nil || r.ReportError != "" || readRecord(t, dir).Stdin != "do it" {
		t.Errorf("without --report: %s, prompt %q", out.stdout, readRecord(t, dir).Stdin)
	}

	out = call{path: vendor(t, "ok"), args: submitArgs("--json", "--report", "do it")}.run(t)
	if r := decodeOne[reportResult](t, out.stdout); out.code != 0 || r.Report != nil || !strings.Contains(r.ReportError, "does not end with an AITS-REPORT line") {
		t.Errorf("no report line: %s", out.stdout)
	}
}

// A task carries --report to its run, and a follow-up inherits it.
func TestTaskReport(t *testing.T) {
	q := newQueue(t, "report_blocked")
	id := q.create("do it", "--report")
	q.run("work")
	f := q.after(id, "next")
	q.run("work")
	for _, x := range []string{id, f} {
		out := q.run("show", x, "--json")
		if !strings.Contains(out.stdout, `"report":{"state":"blocked"`) || !strings.Contains(out.stdout, `"report":true`) {
			t.Errorf("task %s: %s", x, out.stdout)
		}
	}
}
