package shell

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseClaude(t *testing.T) {
	const initLine = `{"type":"system","subtype":"init","session_id":"s1"}`
	const resultLine = `{"type":"result","is_error":false,"result":"the answer","session_id":"s1"}`
	tests := []struct {
		name string
		in   string
		want events
	}{
		{"stream", initLine + "\n" + `{"type":"assistant","session_id":"s1","message":{}}` + "\n" + resultLine + "\n",
			events{sessionID: "s1", answer: "the answer", done: true}},
		{"first session id wins", initLine + "\n" + `{"type":"result","result":"a","session_id":"s2"}` + "\n",
			events{sessionID: "s1", answer: "a", done: true}},
		{"session id only on result", `{"type":"result","result":"a","session_id":"s2"}`,
			events{sessionID: "s2", answer: "a", done: true}},
		{"blank lines ignored", "\n\n" + resultLine + "\n\n", events{sessionID: "s1", answer: "the answer", done: true}},
		{"error result", `{"type":"result","is_error":true,"result":"bad","session_id":"s1"}`,
			events{sessionID: "s1", failure: "bad", done: true, failed: true}},
		{"not json", "hello\n" + resultLine, events{sessionID: "s1", answer: "the answer", done: true, badLine: true}},
		{"error reported in errors", `{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"s1","errors":["No conversation found","second"]}`,
			events{sessionID: "s1", failure: "No conversation found; second", done: true, failed: true}},
		{"errors of another shape are ignored", `{"type":"result","is_error":true,"session_id":"s1","errors":[{"code":1}]}`,
			events{sessionID: "s1", done: true, failed: true}},
		{"session id after a bad line", "hello\n" + initLine, events{sessionID: "s1", badLine: true}},
		{"json array", "[1,2]\n", events{badLine: true}},
		{"json null", "null\n", events{badLine: true}},
		{"result of wrong type", `{"type":"result","result":42}`, events{badLine: true}},
		{"no result event", initLine, events{sessionID: "s1"}},
		{"denials", `{"type":"result","result":"a","session_id":"s1","permission_denials":[{"tool_name":"Write","tool_input":{}},{"tool_name":"Bash"}]}`,
			events{sessionID: "s1", answer: "a", done: true, denied: []string{"Write", "Bash"}, calls: []DeniedCall{{Tool: "Write"}, {Tool: "Bash"}}}},
		{"denied calls", `{"type":"result","result":"a","session_id":"s1","permission_denials":[` +
			`{"tool_name":"Bash","tool_input":{"description":"d","command":"ls $(pwd)"}},` +
			`{"tool_name":"Write","tool_input":{"content":"secret","file_path":"/a"}},` +
			`{"tool_name":"Grep","tool_input":{"pattern":"x","path":"/src"}},` +
			`{"tool_name":"WebFetch","tool_input":{"url":"https://example.com","prompt":"p"}},` +
			`{"tool_name":"Bash","tool_input":{"command":42}},` +
			"{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"bad \xff\"}}]}",
			events{sessionID: "s1", answer: "a", done: true, denied: []string{"Bash", "Write", "Grep", "WebFetch", "Bash", "Bash"},
				calls: []DeniedCall{{"Bash", "ls $(pwd)"}, {"Write", "/a"}, {"Grep", "/src"}, {"WebFetch", "https://example.com"}, {"Bash", ""}, {"Bash", "bad \uFFFD"}}}},
		{"denials of every turn", `{"type":"result","result":"wait","session_id":"s1","permission_denials":[{"tool_name":"Bash","tool_input":{"command":"for f in *; do :; done"}}]}` + "\n" +
			`{"type":"result","result":"done","session_id":"s1","permission_denials":[{"tool_name":"Write","tool_input":{"file_path":"/a"}}]}` + "\n" +
			`{"type":"result","result":"final","session_id":"s1","permission_denials":[]}`,
			events{sessionID: "s1", answer: "final", done: true, denied: []string{"Bash", "Write"},
				calls: []DeniedCall{{"Bash", "for f in *; do :; done"}, {"Write", "/a"}}}},
		{"denials on an error", `{"type":"result","is_error":true,"result":"bad","session_id":"s1","permission_denials":[{"tool_name":"Write"}]}`,
			events{sessionID: "s1", failure: "bad", done: true, failed: true, denied: []string{"Write"}, calls: []DeniedCall{{Tool: "Write"}}}},
		{"no denials", `{"type":"result","result":"a","session_id":"s1","permission_denials":[]}`,
			events{sessionID: "s1", answer: "a", done: true}},
		{"null denials", `{"type":"result","result":"a","session_id":"s1","permission_denials":null}`,
			events{sessionID: "s1", answer: "a", done: true}},
		{"denial without a tool", `{"type":"result","result":"a","session_id":"s1","permission_denials":[{"tool_use_id":"x"}]}`,
			events{sessionID: "s1", badLine: true}},
		{"second denial without a tool", `{"type":"result","result":"a","session_id":"s1","permission_denials":[{"tool_name":"Write"},{}]}`,
			events{sessionID: "s1", badLine: true}},
		{"denials of wrong type", `{"type":"result","result":"a","session_id":"s1","permission_denials":"Write"}`,
			events{sessionID: "s1", badLine: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := readEvents(strings.NewReader(tt.in), parseClaude, nil); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("readEvents = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReadEventsSizeLimit(t *testing.T) {
	// line returns a JSON object exactly n bytes long.
	line := func(n int) string {
		const head, tail = `{"type":"assistant","pad":"`, `"}`
		return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
	}
	if ev := readEvents(strings.NewReader(line(maxEventSize)+"\n"), parseClaude, nil); ev.tooLarge || ev.badLine {
		t.Errorf("a %d-byte event was rejected: tooLarge=%v badLine=%v", maxEventSize, ev.tooLarge, ev.badLine)
	}
	if ev := readEvents(strings.NewReader(line(maxEventSize+1)+"\n"), parseClaude, nil); !ev.tooLarge {
		t.Errorf("a %d-byte event was accepted", maxEventSize+1)
	}
}

// TestParseClaudeBackground follows the task events of Claude Code 2.1.316
// runs (#139): only a background agent or workflow stopped after the agent's
// last message makes the run incomplete.
func TestParseClaudeBackground(t *testing.T) {
	const (
		agentStarted    = `{"type":"system","subtype":"task_started","task_id":"a1","task_type":"local_agent","description":"Run slow.sh","is_backgrounded":true,"session_id":"s1"}`
		workflowStarted = `{"type":"system","subtype":"task_started","task_id":"w1","task_type":"local_workflow","description":"run slow","session_id":"s1"}`
		shellStarted    = `{"type":"system","subtype":"task_started","task_id":"b1","task_type":"local_bash","description":"sleep","is_backgrounded":true,"session_id":"s1"}`
		ownedStarted    = `{"type":"system","subtype":"task_started","task_id":"b2","task_type":"local_agent","owned_by_subagent":true,"description":"nested","is_backgrounded":true,"session_id":"s1"}`
		fgStarted       = `{"type":"system","subtype":"task_started","task_id":"f1","task_type":"local_agent","description":"fg","is_backgrounded":false,"session_id":"s1"}`
		assistant       = `{"type":"assistant","session_id":"s1","message":{}}`
		result          = `{"type":"result","result":"a","session_id":"s1"}`
		killedResult    = `{"type":"result","result":"a","session_id":"s1","subagent_stats":{"killed":{"parent":0,"user":0,"system":1}}}`
	)
	stopped := func(id string) string {
		return `{"type":"system","subtype":"task_notification","task_id":"` + id + `","status":"stopped","summary":"s","session_id":"s1"}`
	}
	completed := `{"type":"system","subtype":"task_notification","task_id":"a1","status":"completed","session_id":"s1"}`
	tests := []struct {
		name    string
		lines   []string
		stopped []string
		killed  bool
	}{
		{"agent stopped after the last message", []string{agentStarted, assistant, stopped("a1"), killedResult}, []string{"Run slow.sh"}, true},
		{"workflow stopped, not counted as killed", []string{workflowStarted, assistant, stopped("w1"), result}, []string{"run slow"}, false},
		{"agent completed", []string{agentStarted, assistant, completed, assistant, result, result}, nil, false},
		{"agent stopped, then the agent went on", []string{agentStarted, stopped("a1"), assistant, result}, nil, false},
		{"background command stopped at exit", []string{shellStarted, assistant, result, stopped("b1")}, nil, false},
		{"a subagent's own task", []string{ownedStarted, assistant, stopped("b2"), result}, nil, false},
		{"foreground agent", []string{fgStarted, assistant, stopped("f1"), result}, nil, false},
		{"unknown task", []string{assistant, stopped("zz"), result}, nil, false},
		{"killed count alone", []string{assistant, killedResult}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := readEvents(strings.NewReader(strings.Join(tt.lines, "\n")), parseClaude, nil)
			if ev.badLine || !reflect.DeepEqual(ev.stopped, tt.stopped) || ev.killed != tt.killed {
				t.Errorf("stopped %q, killed %v, badLine %v; want %q, %v", ev.stopped, ev.killed, ev.badLine, tt.stopped, tt.killed)
			}
		})
	}
	long := strings.Replace(agentStarted, "Run slow.sh", strings.Repeat("é", 200), 1)
	ev := readEvents(strings.NewReader(long+"\n"+stopped("a1")+"\n"+result), parseClaude, nil)
	if len(ev.stopped) != 1 || len(ev.stopped[0]) > stoppedMax || !strings.HasSuffix(ev.stopped[0], "…") {
		t.Errorf("a long description was kept as %q", ev.stopped)
	}
}
