package shell

import "encoding/json"

// parseCodex reads one line of `codex exec --json`. The thread id is the
// session id; the text of the last agent_message item is the answer;
// turn.completed or turn.failed ends the run. Other events, including the
// "error" retry notices Codex sends before it gives up, carry nothing aits
// keeps.
func parseCodex(ev *events, line []byte) bool {
	var h *struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &h); err != nil || h == nil {
		return false
	}
	switch h.Type {
	case "thread.started":
		var e struct {
			ThreadID string `json:"thread_id"`
		}
		if json.Unmarshal(line, &e) != nil {
			return false
		}
		ev.setSession(e.ThreadID)
	case "item.completed":
		var e struct {
			Item struct {
				Type string          `json:"type"`
				Text json.RawMessage `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(line, &e) != nil {
			return false
		}
		if e.Item.Type == "agent_message" {
			var text string
			if json.Unmarshal(e.Item.Text, &text) != nil {
				return false
			}
			ev.answer = text
		}
	case "turn.completed":
		var e struct {
			Usage *struct {
				InputTokens       int64 `json:"input_tokens"`
				CachedInputTokens int64 `json:"cached_input_tokens"`
				OutputTokens      int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(line, &e) != nil {
			return false
		}
		if u := e.Usage; u != nil { // per turn: they add up
			if ev.usage == nil {
				ev.usage = &Usage{}
			}
			ev.usage.InputTokens += u.InputTokens
			ev.usage.CachedInputTokens += u.CachedInputTokens
			ev.usage.OutputTokens += u.OutputTokens
		}
		ev.done = true
	case "turn.failed":
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(line, &e) != nil {
			return false
		}
		ev.done, ev.failed, ev.failure = true, true, e.Error.Message
	}
	return true
}
