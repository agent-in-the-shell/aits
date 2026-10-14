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
		p, ok := parse([]byte(l), i == 0)
		if ok != c.ok[i] {
			t.Errorf("%s: line %d ok = %v, want %v", c.name, i, ok, c.ok[i])
		}
		if p.notSession {
			t.Errorf("%s: line %d marks the file as not a session", c.name, i)
		}
		if id == "" {
			id = p.id
		}
		if cwd == "" {
			cwd = p.cwd
		}
		texts = append(texts, p.texts...)
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
		{"text the CLI writes itself is not searched", []string{
			`{"type":"user",` + head + `,"message":{"content":"<bash-stdout>API_TOKEN=abc</bash-stdout>"}}`,
			`{"type":"user",` + head + `,"message":{"content":"<bash-stderr>boom</bash-stderr>"}}`,
			`{"type":"user",` + head + `,"message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]}}`,
			`{"type":"user",` + head + `,"isCompactSummary":true,"isVisibleInTranscriptOnly":true,"message":{"content":"This session is being continued from a previous conversation"}}`,
			`{"type":"assistant",` + head + `,"isApiErrorMessage":true,"message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: 500 upstream body"}]}}`,
			`{"type":"assistant",` + head + `,"message":{"model":"<synthetic>","content":[{"type":"text","text":"No response requested."}]}}`,
			`{"type":"user",` + head + `,"message":{"content":"<bash-input>ls</bash-input>"}}`},
			[]bool{true, true, true, true, true, true, true}, "s1", "/w", []string{"<bash-input>ls</bash-input>"}},
		{"prompts queued while the agent works are searched", []string{
			`{"type":"attachment",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt","prompt":"also check the docs","origin":{"kind":"human"}}}`,
			`{"type":"attachment",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt","prompt":"no origin recorded"}}`,
			`{"type":"attachment",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt","prompt":"from another agent","isMeta":true,"origin":{"kind":"task"}}}`,
			`{"type":"attachment",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt","prompt":"agent origin","origin":{"kind":"agent"}}}`,
			`{"type":"attachment",` + head + `,"attachment":{"type":"queued_command","commandMode":"bash","prompt":"ls -la"}}`,
			`{"type":"attachment",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt","prompt":"<system-reminder>x"}}`,
			`{"type":"attachment",` + head + `,"attachment":{"type":"file","content":"secret file"}}`,
			`{"type":"attachment",` + head + `,"attachment":"a string"}`},
			[]bool{true, true, true, true, true, true, true, true}, "s1", "/w", []string{"also check the docs", "no origin recorded"}},
		{"a queued prompt given as blocks is searched by its text blocks", []string{
			`{"type":"attachment",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt","prompt":[{"type":"text","text":"blockqueued see this"},{"type":"image","source":{"type":"base64","data":"iVBORw0KGgo="}}],"origin":{"kind":"human"}}}`,
			`{"type":"attachment",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt","prompt":[{"type":"text","text":"<system-reminder>x"},{"type":"text","text":"my own words"}]}}`,
			`{"type":"attachment",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt","prompt":[{"type":"text","text":"from an agent"}],"origin":{"kind":"agent"}}}`,
			`{"type":"attachment",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt","prompt":null}}`,
			`{"type":"attachment",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt"}}`},
			[]bool{true, true, true, true, true}, "s1", "/w", []string{"blockqueued see this", "my own words"}},
		{"a queued prompt of another shape is not usable", []string{
			`{"type":"attachment",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt","prompt":7}}`,
			`{"type":"attachment",` + head + `,"attachment":{"type":"queued_command","commandMode":"prompt","prompt":{}}}`},
			[]bool{false, false}, "s1", "/w", nil},
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
		{"text Codex writes itself is not searched", []string{meta,
			msg("user", "input_text", "# AGENTS.md instructions for /w"),
			msg("user", "input_text", "<user_instructions>be terse</user_instructions>"),
			msg("user", "input_text", "<subagent_notification>child said x</subagent_notification>"),
			msg("user", "input_text", "<turn_aborted>"),
			msg("user", "input_text", "<user_action>review</user_action>"),
			msg("user", "input_text", "<image name=[Image #1]>"),
			msg("user", "input_text", "</image>"),
			msg("user", "input_text", "<recommended_plugins>calendar, notion</recommended_plugins>"),
			msg("user", "input_text", "<codex_internal_context>x</codex_internal_context>"),
			msg("user", "input_text", "my own words")},
			[]bool{true, true, true, true, true, true, true, true, true, true, true}, "c1", "/w", []string{"my own words"}},
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

// A Codex subagent's rollout is not a conversation the user can continue, so
// its file is left out, like Claude's subagent transcripts.
func TestParseCodexSubagent(t *testing.T) {
	for line, want := range map[string]bool{
		`{"type":"session_meta","payload":{"id":"c2","cwd":"/w","source":{"subagent":{"thread_spawn":{"parent_thread_id":"c1"}}}}}`: true,
		`{"type":"session_meta","payload":{"id":"c2","cwd":"/w","source":{"subagent":"review"}}}`:                                   true,
		`{"type":"session_meta","payload":{"id":"c1","cwd":"/w","source":"cli"}}`:                                                   false,
		`{"type":"session_meta","payload":{"id":"c1","cwd":"/w","source":{"exec":{}}}}`:                                             false,
	} {
		if p, ok := parseCodex([]byte(line), true); !ok || p.notSession != want {
			t.Errorf("%s: notSession = %v, ok = %v; want %v", line, p.notSession, ok, want)
		}
	}
}
