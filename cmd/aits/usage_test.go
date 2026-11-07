package main

import "testing"

// Both agents report usage in one vocabulary: claude's cumulative figures
// with its cost, codex's per-turn ones summed, without a cost (#153).
func TestSubmitUsage(t *testing.T) {
	out := call{path: vendor(t, "ok"), args: submitArgs("--json", "hi")}.run(t)
	r := decodeOne[submitResult](t, out.stdout)
	if u := r.Usage; out.code != 0 || u == nil || u.InputTokens != 17 || u.CachedInputTokens != 10 || u.OutputTokens != 3 || u.CostUSD == nil || *u.CostUSD != 0.0125 {
		t.Errorf("claude: code=%d usage=%+v", out.code, r.Usage)
	}
	out = call{path: vendorAs(t, "codex", "ok"), args: agentArgs("codex", "--json", "hi")}.run(t)
	r = decodeOne[submitResult](t, out.stdout)
	if u := r.Usage; out.code != 0 || u == nil || u.InputTokens != 1 || u.OutputTokens != 1 || u.CostUSD != nil {
		t.Errorf("codex: code=%d usage=%+v", out.code, r.Usage)
	}
	out = call{path: vendor(t, "agent_error"), args: submitArgs("--json", "hi")}.run(t)
	if r := decodeOne[submitResult](t, out.stdout); r.Usage != nil {
		t.Errorf("a run with no usage reported: %+v", r.Usage)
	}
	q := newQueue(t, "ok")
	id := q.create("hi")
	q.run("work")
	if r := decodeOne[taskRecord](t, q.run("show", id, "--json").stdout); r.Run == nil || r.Run.Usage == nil || r.Run.Usage.OutputTokens != 3 {
		t.Errorf("task run usage: %+v", r.Run)
	}
}
