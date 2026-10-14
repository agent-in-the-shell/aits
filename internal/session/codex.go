package session

import "encoding/json"

// codexInjected starts user text that Codex writes by itself: environment and
// instruction blocks (AGENTS.md included), subagent notifications, turn
// markers, and the tags around a pasted image.
var codexInjected = []string{
	"<environment_context>", "<user_instructions>", "# AGENTS.md instructions",
	"<subagent_notification>", "<turn_aborted>", "<user_action>", "<image", "</image>",
}

// parseCodex reads one line of a Codex rollout file. The session_meta line
// names the session and its directory, and marks a subagent's rollout;
// response_item messages carry the text. Other lines are ignored, whatever
// their shape.
func parseCodex(line []byte) (parsed, bool) {
	// One decode per line (see parseClaude); payload fields are raw because
	// each line type gives them a different shape.
	var e struct {
		Type    string `json:"type"`
		Payload struct {
			ID      json.RawMessage `json:"id"`
			Cwd     json.RawMessage `json:"cwd"`
			Source  json.RawMessage `json:"source"`
			Type    json.RawMessage `json:"type"`
			Role    json.RawMessage `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &e) != nil {
		return parsed{}, false
	}
	switch e.Type {
	case "session_meta":
		p := parsed{id: rawString(e.Payload.ID), cwd: rawString(e.Payload.Cwd)}
		if p.id == "" && len(e.Payload.ID) > 0 {
			return parsed{}, false // an id that is not a string
		}
		var source map[string]json.RawMessage
		if json.Unmarshal(e.Payload.Source, &source) == nil {
			_, p.notSession = source["subagent"]
		}
		return p, true
	case "response_item":
		role := rawString(e.Payload.Role)
		if rawString(e.Payload.Type) != "message" || (role != "user" && role != "assistant") || len(e.Payload.Content) == 0 {
			return parsed{}, true
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(e.Payload.Content, &blocks) != nil {
			return parsed{}, false
		}
		var p parsed
		for _, b := range blocks {
			if b.Type != "input_text" && b.Type != "output_text" {
				continue
			}
			if role == "user" && injected(b.Text, codexInjected) {
				continue
			}
			p.texts = append(p.texts, b.Text)
		}
		return p, true
	}
	return parsed{}, true
}
