package session

import "encoding/json"

// codexInjected starts user text that Codex adds by itself.
var codexInjected = []string{"<environment_context>"}

// parseCodex reads one line of a Codex rollout file. The session_meta line
// names the session and its directory; response_item messages carry the text.
// Other lines are ignored, whatever their shape.
func parseCodex(line []byte) (id, cwd string, texts []string, ok bool) {
	// One decode per line (see parseClaude); payload fields are raw because
	// each line type gives them a different shape.
	var e struct {
		Type    string `json:"type"`
		Payload struct {
			ID      json.RawMessage `json:"id"`
			Cwd     json.RawMessage `json:"cwd"`
			Type    json.RawMessage `json:"type"`
			Role    json.RawMessage `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &e) != nil {
		return "", "", nil, false
	}
	switch e.Type {
	case "session_meta":
		id, cwd = rawString(e.Payload.ID), rawString(e.Payload.Cwd)
		if id == "" && len(e.Payload.ID) > 0 {
			return "", "", nil, false // an id that is not a string
		}
		return id, cwd, nil, true
	case "response_item":
		role := rawString(e.Payload.Role)
		if rawString(e.Payload.Type) != "message" || (role != "user" && role != "assistant") || len(e.Payload.Content) == 0 {
			return "", "", nil, true
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(e.Payload.Content, &blocks) != nil {
			return "", "", nil, false
		}
		for _, b := range blocks {
			if b.Type != "input_text" && b.Type != "output_text" {
				continue
			}
			if role == "user" && injected(b.Text, codexInjected) {
				continue
			}
			texts = append(texts, b.Text)
		}
		return "", "", texts, true
	}
	return "", "", nil, true
}
