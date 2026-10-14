package session

import "encoding/json"

// codexInjected starts user text that Codex writes by itself: environment,
// instruction and plugin blocks (AGENTS.md included), subagent notifications,
// turn markers, and the tags around a pasted image.
var codexInjected = []string{
	"<environment_context>", "<user_instructions>", "# AGENTS.md instructions",
	"<recommended_plugins>", "<codex_internal_context>",
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
		texts, ok := blockTexts(e.Payload.Content, "input_text", "output_text")
		if !ok {
			return parsed{}, false
		}
		if role == "user" {
			texts = ownText(texts, codexInjected)
		}
		return parsed{message: true, texts: texts}, true
	}
	return parsed{}, true
}
