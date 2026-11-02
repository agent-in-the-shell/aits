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

// claudeTask holds the fields aits reads from Claude Code's task events:
// task_started, for a task the agent runs, and task_notification, for how it
// ended.
type claudeTask struct {
	Subtype        string `json:"subtype"`
	TaskID         string `json:"task_id"`
	TaskType       string `json:"task_type"`
	Description    string `json:"description"`
	IsBackgrounded *bool  `json:"is_backgrounded"`
	OwnedBySub     bool   `json:"owned_by_subagent"`
	Status         string `json:"status"`
}

type claudeResult struct {
	IsError           bool            `json:"is_error"`
	Result            string          `json:"result"`
	Errors            json.RawMessage `json:"errors"` // where some errors are reported instead of result
	PermissionDenials []struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	} `json:"permission_denials"`
	SubagentStats struct {
		Killed struct {
			System int `json:"system"`
		} `json:"killed"`
	} `json:"subagent_stats"`
}

// maxBackground bounds the background tasks aits remembers in one run, and
// stoppedMax the bytes kept of each one's description (P8).
const (
	maxBackground = 1000
	stoppedMax    = 200
)

// parseClaude reads one line of `claude -p --output-format stream-json`: any
// event may carry session_id, and a "result" event ends a turn. A run has one
// more turn for each background task that reports back after the first, so
// the last result holds the answer, while each result lists only its own
// turn's denials: those add up.
//
// Claude Code waits a while for background work after the last turn, then
// stops it and still reports success (#139). Such a run is told by its task
// events: a background agent or workflow of the agent's own that is stopped
// with no message of the agent after it, which then never saw its work. A
// background command is not counted: Claude Code stops those at exit by
// design, like a server the agent left running.
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
	if json.Unmarshal(h.Type, &typ) != nil {
		return true
	}
	switch typ {
	case "assistant":
		ev.stopped = nil // the agent went on after the stop, so it knew
		return true
	case "system":
		parseClaudeTask(ev, line)
		return true
	case "result":
	default:
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
	ev.killed = ev.killed || res.SubagentStats.Killed.System > 0
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

// parseClaudeTask records a task the agent started in the background, and
// one of those being stopped. An event of another shape is not a task event
// aits knows, and is skipped like any other system event.
func parseClaudeTask(ev *events, line []byte) {
	var t claudeTask
	if json.Unmarshal(line, &t) != nil || t.TaskID == "" {
		return
	}
	switch t.Subtype {
	case "task_started":
		// A workflow has no is_backgrounded: it always runs in the background.
		if t.TaskType == "local_bash" || t.OwnedBySub || t.IsBackgrounded != nil && !*t.IsBackgrounded {
			return
		}
		if ev.background == nil {
			ev.background = map[string]string{}
		}
		if len(ev.background) < maxBackground {
			ev.background[t.TaskID] = cut(t.Description, stoppedMax)
		}
	case "task_notification":
		if desc, ok := ev.background[t.TaskID]; ok && t.Status == "stopped" {
			if desc == "" {
				desc = t.TaskID
			}
			ev.stopped = append(ev.stopped, desc)
		}
	}
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
		return cut(s, deniedInputMax)
	}
	return ""
}

// cut returns s as valid UTF-8, cut to at most max bytes on a rune boundary
// with "…" when it was longer.
func cut(s string, max int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= max {
		return s
	}
	n := max - len("…")
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
