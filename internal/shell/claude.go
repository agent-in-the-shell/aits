package shell

import (
	"encoding/json"
	"strings"
)

// claudeHeader holds the fields aits reads from every Claude Code event.
// Decoding into it skips all other fields without copying them, however large
// they are.
type claudeHeader struct {
	Type      json.RawMessage `json:"type"`
	SessionID json.RawMessage `json:"session_id"`
}

type claudeResult struct {
	IsError bool            `json:"is_error"`
	Result  string          `json:"result"`
	Errors  json.RawMessage `json:"errors"` // where some errors are reported instead of result
	// The tool calls refused for want of the user's approval: claude -p never
	// asks.
	PermissionDenials []struct {
		ToolName string `json:"tool_name"`
	} `json:"permission_denials"`
}

// parseClaude reads one line of `claude -p --output-format stream-json`: any
// event may carry session_id, and the "result" event ends the run.
func parseClaude(ev *events, line []byte) bool {
	var h *claudeHeader // stays nil for the JSON literal null
	if err := json.Unmarshal(line, &h); err != nil || h == nil {
		return false
	}
	var id string
	if json.Unmarshal(h.SessionID, &id) == nil {
		ev.setSession(id)
	}
	var typ string
	if json.Unmarshal(h.Type, &typ) != nil || typ != "result" {
		return true
	}
	var res claudeResult
	if err := json.Unmarshal(line, &res); err != nil {
		return false
	}
	for _, d := range res.PermissionDenials {
		if d.ToolName == "" {
			return false
		}
		ev.denied = append(ev.denied, d.ToolName)
	}
	ev.done = true
	if res.IsError {
		ev.failed, ev.failure = true, res.Result
		var errs []string
		if ev.failure == "" && json.Unmarshal(res.Errors, &errs) == nil {
			ev.failure = strings.Join(errs, "; ")
		}
	} else {
		ev.answer = res.Result
	}
	return true
}
