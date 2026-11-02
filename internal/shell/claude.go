package shell

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// claudeHeader holds the fields aits reads from every Claude Code event.
// Decoding into it skips all other fields without copying them, however large
// they are.
type claudeHeader struct {
	Type      json.RawMessage `json:"type"`
	SessionID json.RawMessage `json:"session_id"`
}

type claudeResult struct {
	IsError           bool            `json:"is_error"`
	Result            string          `json:"result"`
	Errors            json.RawMessage `json:"errors"` // where some errors are reported instead of result
	PermissionDenials []struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	} `json:"permission_denials"`
}

// parseClaude reads one line of `claude -p --output-format stream-json`: any
// event may carry session_id, and a "result" event ends a turn. A run has one
// more turn for each background task that reports back after the first, so
// the last result holds the answer, while each result lists only its own
// turn's denials: those add up.
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
	var denied []string // this result's, kept only if every entry names its tool (P6)
	var calls []DeniedCall
	for _, d := range res.PermissionDenials {
		if d.ToolName == "" {
			return false
		}
		denied = append(denied, d.ToolName)
		calls = append(calls, DeniedCall{Tool: d.ToolName, Input: mainInput(d.ToolInput)})
	}
	ev.denied, ev.calls = append(ev.denied, denied...), append(ev.calls, calls...)
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

// deniedInputMax bounds a denied call's input in a result (P8).
const deniedInputMax = 500

// inputKeys are the arguments that tell what a refused call wanted, first
// match wins: Bash's command, the file tools' paths, WebFetch's URL, Glob's
// and Grep's pattern. The rest of the input, such as a Write's content, is
// never copied (P8).
var inputKeys = []string{"command", "file_path", "notebook_path", "path", "url", "pattern"}

// mainInput returns the first string among inputKeys in the call's input,
// cut to deniedInputMax bytes on a UTF-8 boundary, or "". An input of another
// shape has none: the denial itself still counts.
func mainInput(raw json.RawMessage) string {
	var input map[string]json.RawMessage
	if json.Unmarshal(raw, &input) != nil {
		return ""
	}
	for _, k := range inputKeys {
		var s string
		if json.Unmarshal(input[k], &s) != nil || s == "" {
			continue
		}
		s = strings.ToValidUTF8(s, "\uFFFD")
		if len(s) > deniedInputMax {
			cut := deniedInputMax - len("…")
			for cut > 0 && !utf8.RuneStart(s[cut]) {
				cut--
			}
			s = s[:cut] + "…"
		}
		return s
	}
	return ""
}
