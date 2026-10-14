package session

import "encoding/json"

// codexInjected starts user text that Codex adds by itself.
var codexInjected = []string{"<environment_context>"}

// parseCodex reads one line of a Codex rollout file. The session_meta line
// names the session and its directory; response_item messages carry the text.
// Other lines are ignored, whatever their shape.
func parseCodex(line []byte) (id, cwd string, texts []string, ok bool) {
	var h struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(line, &h) != nil {
		return "", "", nil, false
	}
	switch h.Type {
	case "session_meta":
		var p struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(h.Payload, &p) != nil {
			return "", "", nil, false
		}
		return p.ID, p.Cwd, nil, true
	case "response_item":
		var p struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(h.Payload, &p) != nil {
			return "", "", nil, false
		}
		if p.Type != "message" || (p.Role != "user" && p.Role != "assistant") || len(p.Content) == 0 {
			return "", "", nil, true
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(p.Content, &blocks) != nil {
			return "", "", nil, false
		}
		for _, b := range blocks {
			if b.Type != "input_text" && b.Type != "output_text" {
				continue
			}
			if p.Role == "user" && injected(b.Text, codexInjected) {
				continue
			}
			texts = append(texts, b.Text)
		}
		return "", "", texts, true
	}
	return "", "", nil, true
}
