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
func parseClaude(line []byte, _ bool) (parsed, bool) {
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
		Entrypoint        json.RawMessage `json:"entrypoint"`
		Message           json.RawMessage `json:"message"`
		Attachment        json.RawMessage `json:"attachment"`
	}
	if json.Unmarshal(line, &e) != nil {
		return parsed{}, false
	}
	p := parsed{id: rawString(e.SessionID), cwd: rawString(e.Cwd), source: rawString(e.Entrypoint), message: e.Type == "user" || e.Type == "assistant"}
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
		texts, ok := claudeTexts(m.Content)
		if !ok {
			return p, false
		}
		if e.Type == "user" {
			texts = ownText(texts, claudeInjected)
		}
		p.texts = texts
	case "attachment":
		texts, ok := queuedPrompt(e.Attachment)
		if !ok {
			return p, false
		}
		p.texts = ownText(texts, claudeInjected)
	}
	return p, true
}

// queuedPrompt returns the text of a prompt the user typed while the agent was
// working, as a string or as content blocks (with a pasted image), and nothing
// for any other attachment (files, agent-to-agent messages, queued shell
// commands); ok is false when such a prompt has another shape.
func queuedPrompt(raw json.RawMessage) ([]string, bool) {
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
		return nil, true
	}
	return claudeTexts(a.Prompt)
}

// claudeTexts returns the searched text of a message's content: the content
// itself when it is a string, else the text of its "text" blocks. The first
// byte decides, so a large content is decoded once.
func claudeTexts(content json.RawMessage) ([]string, bool) {
	switch {
	case len(content) == 0 || string(content) == "null":
		return nil, true
	case content[0] == '"':
		var s string
		if json.Unmarshal(content, &s) != nil {
			return nil, false
		}
		return []string{s}, true
	}
	return blockTexts(content, "text")
}

// rawString returns raw as a string, or "" when it is not a JSON string.
func rawString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}
