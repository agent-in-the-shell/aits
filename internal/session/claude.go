package session

import "encoding/json"

// claudeInjected starts user text that Claude Code adds by itself.
var claudeInjected = []string{"<task-notification>", "<local-command-", "<command-", "<system-reminder>"}

// parseClaude reads one line of a Claude Code transcript. Every event may
// carry sessionId and cwd; user and assistant events carry the message text.
// Events of other types are ignored, whatever their shape.
func parseClaude(line []byte) (id, cwd string, texts []string, ok bool) {
	// One decode per line: Unmarshal validates the whole line first, and
	// lines carrying tool output can be megabytes. Fields are raw so that
	// events of other shapes are not errors.
	var e struct {
		Type        string          `json:"type"`
		SessionID   json.RawMessage `json:"sessionId"`
		Cwd         json.RawMessage `json:"cwd"`
		IsSidechain json.RawMessage `json:"isSidechain"`
		IsMeta      json.RawMessage `json:"isMeta"`
		Message     json.RawMessage `json:"message"`
	}
	if json.Unmarshal(line, &e) != nil {
		return "", "", nil, false
	}
	id, cwd = rawString(e.SessionID), rawString(e.Cwd)
	if e.Type != "user" && e.Type != "assistant" || string(e.IsSidechain) == "true" || string(e.IsMeta) == "true" {
		return id, cwd, nil, true
	}
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if len(e.Message) > 0 && json.Unmarshal(e.Message, &m) != nil {
		return id, cwd, nil, false
	}
	all, ok := claudeTexts(m.Content)
	if !ok {
		return id, cwd, nil, false
	}
	for _, s := range all {
		if e.Type == "user" && injected(s, claudeInjected) {
			continue
		}
		texts = append(texts, s)
	}
	return id, cwd, texts, true
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
