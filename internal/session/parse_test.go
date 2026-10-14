package session

import (
	"slices"
	"testing"
)

type parseCase struct {
	name    string
	lines   []string
	ok      []bool // per line
	id, cwd string
	texts   []string // searched texts, in order
}

// run feeds c.lines to parse and checks what it reported: the first id and
// cwd named, the searched texts, and which lines were usable.
func run(t *testing.T, parse parser, c parseCase) {
	t.Helper()
	var id, cwd string
	var texts []string
	for i, l := range c.lines {
		lid, lcwd, lt, ok := parse([]byte(l))
		if ok != c.ok[i] {
			t.Errorf("%s: line %d ok = %v, want %v", c.name, i, ok, c.ok[i])
		}
		if id == "" {
			id = lid
		}
		if cwd == "" {
			cwd = lcwd
		}
		texts = append(texts, lt...)
	}
	if id != c.id || cwd != c.cwd {
		t.Errorf("%s: id=%q cwd=%q, want %q %q", c.name, id, cwd, c.id, c.cwd)
	}
	if !slices.Equal(texts, c.texts) {
		t.Errorf("%s: texts = %q, want %q", c.name, texts, c.texts)
	}
}

func TestParseClaude(t *testing.T) {
	const head = `"sessionId":"s1","cwd":"/w"`
	for _, c := range []parseCase{
		{"string content", []string{`{"type":"user",` + head + `,"message":{"role":"user","content":"hello there"}}`},
			[]bool{true}, "s1", "/w", []string{"hello there"}},
		{"text blocks only", []string{`{"type":"assistant",` + head + `,"message":{"content":[{"type":"thinking","thinking":"secret plan"},{"type":"text","text":"answer"},{"type":"tool_use","name":"Bash","input":{"command":"cat .env"}}]}}`},
			[]bool{true}, "s1", "/w", []string{"answer"}},
		{"tool results not searched", []string{`{"type":"user",` + head + `,"message":{"content":[{"type":"tool_result","content":"API_KEY=x"}]}}`},
			[]bool{true}, "s1", "/w", nil},
		{"sidechain and meta skipped", []string{
			`{"type":"user",` + head + `,"isSidechain":true,"message":{"content":"sub"}}`,
			`{"type":"user",` + head + `,"isMeta":true,"message":{"content":"meta"}}`},
			[]bool{true, true}, "s1", "/w", nil},
		{"injected user text skipped", []string{
			`{"type":"user",` + head + `,"message":{"content":"<system-reminder>x</system-reminder>"}}`,
			`{"type":"user",` + head + `,"message":{"content":[{"type":"text","text":"<command-name>/clear</command-name>"}]}}`,
			`{"type":"assistant",` + head + `,"message":{"content":[{"type":"text","text":"<command-name> is fine from the assistant"}]}}`},
			[]bool{true, true, true}, "s1", "/w", []string{"<command-name> is fine from the assistant"}},
		{"other event types", []string{
			`{"type":"summary","summary":"x","leafUuid":"u"}`,
			`{"type":"file-history-snapshot","snapshot":{}}`,
			`{"type":"system",` + head + `,"content":"x"}`},
			[]bool{true, true, true}, "s1", "/w", nil},
		{"identity from the first event that has it", []string{
			`{"type":"summary"}`,
			`{"type":"user","sessionId":"s1","cwd":"/w","message":{"content":"a"}}`,
			`{"type":"user","sessionId":"s2","cwd":"/x","message":{"content":"b"}}`},
			[]bool{true, true, true}, "s1", "/w", []string{"a", "b"}},
		{"unusable lines", []string{`not json`, `{"type":"user",` + head + `,"message":{"content":42}}`},
			[]bool{false, false}, "s1", "/w", nil},
	} {
		run(t, parseClaude, c)
	}
}

func TestParseCodex(t *testing.T) {
	const meta = `{"type":"session_meta","payload":{"id":"c1","cwd":"/w","cli_version":"0.159.2"}}`
	msg := func(role, kind, text string) string {
		return `{"type":"response_item","payload":{"type":"message","role":"` + role + `","content":[{"type":"` + kind + `","text":"` + text + `"}]}}`
	}
	for _, c := range []parseCase{
		{"messages", []string{meta, msg("user", "input_text", "question"), msg("assistant", "output_text", "answer")},
			[]bool{true, true, true}, "c1", "/w", []string{"question", "answer"}},
		{"developer and injected context skipped", []string{meta,
			msg("developer", "input_text", "<permissions instructions>"),
			msg("user", "input_text", "<environment_context><cwd>/w</cwd></environment_context>")},
			[]bool{true, true, true}, "c1", "/w", nil},
		{"other line types", []string{meta,
			`{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"cat .env\"}"}}`,
			`{"type":"response_item","payload":{"type":"function_call_output","output":"API_KEY=x"}}`,
			`{"type":"response_item","payload":{"type":"reasoning","summary":[]}}`,
			`{"type":"event_msg","payload":{"type":"agent_message","message":"dup"}}`,
			`{"type":"turn_context","payload":{"cwd":"/w"}}`},
			[]bool{true, true, true, true, true, true}, "c1", "/w", nil},
		{"unusable lines", []string{`not json`, `{"type":"session_meta","payload":{"id":7}}`,
			`{"type":"response_item","payload":{"type":"message","role":"user","content":"not a list"}}`},
			[]bool{false, false, false}, "", "", nil},
	} {
		run(t, parseCodex, c)
	}
}
