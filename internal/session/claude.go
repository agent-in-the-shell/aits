package session

import "encoding/json"

// claudeInjected starts user text that Claude Code writes by itself: system
// reminders, slash-command and task notifications, "!" shell output, and the
// interruption marker.
var claudeInjected = []string{
	"<task-notification>", "<local-command-", "<command-", "<system-reminder>",
	"<bash-stdout>", "<bash-stderr>", "[Request interrupted",
}

// parseClaude reads one line of a Claude Code transcript. Every event may
// carry sessionId and cwd; user and assistant events carry the message text,
// and a prompt the user queued while the agent was working is an attachment.
// Events of other types are ignored, whatever their shape.
func parseClaude(line []byte) (parsed, bool) {
	// One decode per line: Unmarshal validates the whole line first, and
	// lines carrying tool output can be megabytes. Fields are raw so that
	// events of other shapes are not errors.
	var e struct {
		Type              string          `json:"type"`
		SessionID         json.RawMessage `json:"sessionId"`
		Cwd               json.RawMessage `json:"cwd"`
		IsSidechain       json.RawMessage `json:"isSidechain"`
		IsMeta            json.RawMessage `json:"isMeta"`
		IsCompactSummary  json.RawMessage `json:"isCompactSummary"`
		TranscriptOnly    json.RawMessage `json:"isVisibleInTranscriptOnly"`
		IsAPIErrorMessage json.RawMessage `json:"isApiErrorMessage"`
		Message           json.RawMessage `json:"message"`
		Attachment        json.RawMessage `json:"attachment"`
	}
	if json.Unmarshal(line, &e) != nil {
		return parsed{}, false
	}
	p := parsed{id: rawString(e.SessionID), cwd: rawString(e.Cwd)}
	// Text the CLI writes itself: subagent turns, meta turns, compaction
	// summaries (which restate tool output) and synthesized API errors.
	for _, flag := range []json.RawMessage{e.IsSidechain, e.IsMeta, e.IsCompactSummary, e.TranscriptOnly, e.IsAPIErrorMessage} {
		if string(flag) == "true" {
			return p, true
		}
	}
	switch e.Type {
	case "user", "assistant":
		var m struct {
			Model   json.RawMessage `json:"model"`
			Content json.RawMessage `json:"content"`
		}
		if len(e.Message) > 0 && json.Unmarshal(e.Message, &m) != nil {
			return p, false
		}
		if rawString(m.Model) == "<synthetic>" {
			return p, true
		}
		all, ok := claudeTexts(m.Content)
		if !ok {
			return p, false
		}
		for _, s := range all {
			if e.Type == "user" && injected(s, claudeInjected) {
				continue
			}
			p.texts = append(p.texts, s)
		}
	case "attachment":
		if s := queuedPrompt(e.Attachment); s != "" && !injected(s, claudeInjected) {
			p.texts = append(p.texts, s)
		}
	}
	return p, true
}

// queuedPrompt returns the text of a prompt the user typed while the agent was
// working, or "" for any other attachment (files, agent-to-agent messages,
// queued shell commands).
func queuedPrompt(raw json.RawMessage) string {
	var a struct {
		Type        json.RawMessage `json:"type"`
		CommandMode json.RawMessage `json:"commandMode"`
		Prompt      json.RawMessage `json:"prompt"`
		IsMeta      json.RawMessage `json:"isMeta"`
		Origin      *struct {
			Kind json.RawMessage `json:"kind"`
		} `json:"origin"`
	}
	if json.Unmarshal(raw, &a) != nil || rawString(a.Type) != "queued_command" ||
		rawString(a.CommandMode) != "prompt" || string(a.IsMeta) == "true" ||
		(a.Origin != nil && rawString(a.Origin.Kind) != "human") {
		return ""
	}
	return rawString(a.Prompt)
}

// claudeTexts returns the searched text of a message's content: the content
// itself when it is a string, else the text of its "text" blocks.
func claudeTexts(content json.RawMessage) ([]string, bool) {
	if len(content) == 0 {
		return nil, true
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return []string{s}, true
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return nil, false
	}
	var out []string
	for _, b := range blocks {
		if b.Type == "text" {
			out = append(out, b.Text)
		}
	}
	return out, true
}

// rawString returns raw as a string, or "" when it is not a JSON string.
func rawString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}
