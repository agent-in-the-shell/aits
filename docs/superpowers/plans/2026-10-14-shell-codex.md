# `aits shell submit --agent codex` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Codex CLI as a second agent of `aits shell submit`, with the same result object, statuses and exit codes as Claude Code.

**Architecture:** The line reader, process supervisor and outcome table stay shared. Each agent in the `agents` table gets a `parse` function that turns one JSON line into a vendor-neutral `events` record (`sessionID`, `answer`, `done`, `failed`, `failure`); `decide` reads only that record. A single fake vendor speaks Claude or Codex by its executable name, and the shared functional tests run once per agent.

**Tech Stack:** Go 1.26 standard library only.

**Spec:** `docs/superpowers/specs/2026-10-14-shell-codex-design.md` (builds on `docs/superpowers/specs/2026-10-13-shell-submit-design.md`). Issue: #13.

## Global Constraints

- Standard library only; no new `require` in `go.mod` (P3). `CGO_ENABLED=0`.
- Linux and macOS.
- `gofmt -l .` prints nothing; `go vet ./...` and `go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 -tags live ./...` are clean.
- Codex argv is exactly `exec --json -`; aits never passes `--skip-git-repo-check`, sandbox, approval or model flags (P9).
- The prompt is written to stdin, never argv (P8).
- Base environment allowlist for every agent: `PATH`, `HOME`, `USER`, `LOGNAME`, `LANG`, every `LC_*`, `TZ`, `TMPDIR`, `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`. Claude adds `CLAUDE_CONFIG_DIR`, `ANTHROPIC_API_KEY`; Codex adds `CODEX_HOME`, `OPENAI_API_KEY`. Neither gets the other's.
- Outcome order unchanged from the #2 spec §6; `agent_failed` message prefers the agent's reported error (first line, ≤ 200 bytes) over `the agent exited with code N`.
- Tests run the built binary with a temporary `HOME` and `AITS_HOME` (P12); live tests only behind `//go:build live`.
- Code, docs and commit messages in English. Every commit: leak-check the staged diff, body `Refs #13`.

## Review Focus

1. A Codex stdout that starts with a non-JSON line → `bad_agent_output` that still carries `session_id` (the #4 behaviour must hold for the new parser) — pinned by `TestSubmitBadOutput/codex/not_json` (Task 3).
2. A Codex tool item with a large `aggregated_output` before the final message → still `ok`, answer is the last `agent_message` — pinned by `TestParseCodex/large tool output` (Task 2).
3. `turn.failed` with no `error.message` → `agent_failed` with a non-empty fallback message — pinned by `TestParseCodex/turn failed without message` (Task 2) and `TestFailureMessage/reported error without text` (Task 1).
4. Event types Codex adds in later versions (e.g. `turn.diff`) → ignored, not `bad_agent_output` — pinned by `TestParseCodex/unknown event types` (Task 2).
5. A Codex `error` event that is fatal but is not followed by `turn.failed` (process exits non-zero) → `agent_failed` with the exit-code message and the stderr tail — pinned by `TestSubmitAgentFailed/codex` (Task 3, scenario `exit_nonzero`).

---

### Task 1: Vendor-neutral event record, Claude parser, failure message

**Files:**
- Modify: `internal/shell/events.go` (whole file)
- Create: `internal/shell/claude.go`
- Modify: `internal/shell/agent.go:9-29` (add `parse` field; Claude entry)
- Modify: `internal/shell/outcome.go:17-41` (`decide`), add `failureMessage`
- Modify: `internal/shell/shell.go` (`Submit`: `res.Answer`)
- Modify: `internal/shell/process.go` (`supervise` takes the parser)
- Modify: `internal/shell/testdata/fakeclaude/main.go` (scenarios `agent_error`, `agent_error_exit0`)
- Test: `internal/shell/events_test.go`, `internal/shell/outcome_test.go`, `cmd/aits/e2e_test.go`

**Interfaces:**
- Produces:
  - `type events struct { sessionID, answer, failure string; done, failed, notJSON, tooLarge bool }`
  - `func (ev *events) setSession(id string)`
  - `type parser func(ev *events, line []byte) bool`
  - `func readEvents(r io.Reader, parse parser) events`
  - `func parseClaude(ev *events, line []byte) bool`
  - `agent.parse parser`
  - `func failureMessage(ev events, exitCode int) string`
  - fake scenarios `agent_error` (error report, exit 1) and `agent_error_exit0` (error report, exit 0; replaces `is_error`)

- [ ] **Step 1: Rewrite the event unit tests against the neutral record**

Replace `TestReadEvents` in `internal/shell/events_test.go` with `TestParseClaude`, and pass the parser in `TestReadEventsSizeLimit`:

```go
func TestParseClaude(t *testing.T) {
	const initLine = `{"type":"system","subtype":"init","session_id":"s1"}`
	const resultLine = `{"type":"result","is_error":false,"result":"the answer","session_id":"s1"}`
	tests := []struct {
		name string
		in   string
		want events
	}{
		{"stream", initLine + "\n" + `{"type":"assistant","session_id":"s1","message":{}}` + "\n" + resultLine + "\n",
			events{sessionID: "s1", answer: "the answer", done: true}},
		{"first session id wins", initLine + "\n" + `{"type":"result","result":"a","session_id":"s2"}` + "\n",
			events{sessionID: "s1", answer: "a", done: true}},
		{"session id only on result", `{"type":"result","result":"a","session_id":"s2"}`,
			events{sessionID: "s2", answer: "a", done: true}},
		{"blank lines ignored", "\n\n" + resultLine + "\n\n", events{sessionID: "s1", answer: "the answer", done: true}},
		{"error result", `{"type":"result","is_error":true,"result":"bad","session_id":"s1"}`,
			events{sessionID: "s1", failure: "bad", done: true, failed: true}},
		{"not json", "hello\n" + resultLine, events{sessionID: "s1", answer: "the answer", done: true, notJSON: true}},
		{"session id after a bad line", "hello\n" + initLine, events{sessionID: "s1", notJSON: true}},
		{"json array", "[1,2]\n", events{notJSON: true}},
		{"json null", "null\n", events{notJSON: true}},
		{"result of wrong type", `{"type":"result","result":42}`, events{notJSON: true}},
		{"no result event", initLine, events{sessionID: "s1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := readEvents(strings.NewReader(tt.in), parseClaude); got != tt.want {
				t.Errorf("readEvents = %+v, want %+v", got, tt.want)
			}
		})
	}
}
```

In `TestReadEventsSizeLimit`, change both `readEvents(strings.NewReader(...))` calls to `readEvents(strings.NewReader(...), parseClaude)`. Remove the now-unused `"reflect"` import.

- [ ] **Step 2: Rewrite the outcome unit tests**

Replace `TestDecide` in `internal/shell/outcome_test.go` and add `TestFailureMessage`:

```go
func TestDecide(t *testing.T) {
	okEvents := events{sessionID: "s1", answer: "answer", done: true}
	tests := []struct {
		name string
		obs  observation
		want Status
	}{
		{"ok", observation{events: okEvents}, StatusOK},
		{"stopped first", observation{stopped: true, timedOut: true, exitCode: -1, events: events{tooLarge: true}}, StatusStopped},
		{"timeout before too large", observation{timedOut: true, exitCode: -1, events: events{tooLarge: true}}, StatusTimeout},
		{"too large before exit code", observation{exitCode: -1, events: events{tooLarge: true}}, StatusTooLarge},
		{"exit code before bad output", observation{exitCode: 3, events: events{notJSON: true}}, StatusAgentFailed},
		{"not json", observation{events: events{notJSON: true}}, StatusBadOutput},
		{"not json despite a good result", observation{events: events{sessionID: "s1", answer: "a", done: true, notJSON: true}}, StatusBadOutput},
		{"reported failure", observation{events: events{sessionID: "s1", done: true, failed: true, failure: "boom"}}, StatusAgentFailed},
		{"no final event", observation{events: events{sessionID: "s1", answer: "a"}}, StatusBadOutput},
		{"empty answer", observation{events: events{sessionID: "s1", done: true}}, StatusBadOutput},
		{"no session id", observation{events: events{answer: "answer", done: true}}, StatusBadOutput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, msg := decide(tt.obs)
			if got != tt.want {
				t.Fatalf("decide = %s, want %s", got, tt.want)
			}
			if (got == StatusOK) != (msg == "") {
				t.Errorf("status %s with message %q", got, msg)
			}
		})
	}
}

// TestFailureMessage: the agent's own error beats its exit code, which hides
// the reason (Claude not logged in exits 1 with an empty stderr).
func TestFailureMessage(t *testing.T) {
	tests := []struct {
		name string
		obs  observation
		want string
	}{
		{"reported error with non-zero exit",
			observation{exitCode: 1, events: events{sessionID: "s1", done: true, failed: true, failure: "Not logged in\nmore"}},
			"the agent reported an error: Not logged in"},
		{"reported error with exit 0",
			observation{events: events{sessionID: "s1", done: true, failed: true, failure: "boom"}},
			"the agent reported an error: boom"},
		{"exit code only", observation{exitCode: 3}, "the agent exited with code 3"},
		{"reported error without text", observation{events: events{done: true, failed: true}}, "the agent reported an error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if status, got := decide(tt.obs); status != StatusAgentFailed || got != tt.want {
				t.Errorf("decide = %s, %q; want agent_failed, %q", status, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 3: Add the error-with-exit-1 scenario to the fake and its functional test**

In `internal/shell/testdata/fakeclaude/main.go`, replace the `case "is_error":` block with:

```go
	case "agent_error":
		// Claude Code not logged in: an error result, exit 1, nothing on stderr.
		emit(initEvent())
		emit(result(true, "fake failure\nsecond line"))
		os.Exit(1)
	case "agent_error_exit0":
		emit(initEvent())
		emit(result(true, "fake failure\nsecond line"))
```

In `cmd/aits/e2e_test.go`, replace `TestSubmitAgentReportsError` with:

```go
func TestSubmitAgentReportsError(t *testing.T) {
	for scenario, exit := range map[string]int{"agent_error": 1, "agent_error_exit0": 0} {
		t.Run(scenario, func(t *testing.T) {
			out := call{path: vendor(t, scenario), args: submitArgs("--json", "hi")}.run(t)
			r := decode(t, out.stdout)
			if out.code != 1 || r.Status != "agent_failed" || r.ExitCode == nil || *r.ExitCode != exit || r.Answer != "" ||
				r.Message != "the agent reported an error: fake failure" {
				t.Fatalf("code=%d result=%+v", out.code, r)
			}
		})
	}
}
```

- [ ] **Step 4: Run the tests to see them fail**

Run: `go test -count=1 ./internal/shell 2>&1 | tail -5; go test -count=1 ./cmd/aits -run TestSubmitAgentReportsError 2>&1 | tail -5`
Expected: `internal/shell` fails to build (`undefined: parseClaude`, unknown fields `answer`, `done`, …); `TestSubmitAgentReportsError/agent_error` FAILS with message `the agent exited with code 1`; `agent_error_exit0` passes.

- [ ] **Step 5: Implement the neutral record and the Claude parser**

Replace `internal/shell/events.go` with:

```go
package shell

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// maxEventSize bounds one line of agent output (P8).
const maxEventSize = 16 << 20

// events is what aits keeps of an agent's JSON event stream, in a form that
// does not depend on the vendor. Each agent's parser fills it; every event is
// dropped once parsed.
type events struct {
	sessionID string // the first session id seen
	answer    string // the reply text
	failure   string // the error the agent reported
	done      bool   // the agent's final event was seen
	failed    bool   // the agent reported that the run failed
	notJSON   bool   // a line was not a usable event
	tooLarge  bool   // a line exceeded maxEventSize
}

// setSession records id unless a session id was already seen.
func (ev *events) setSession(id string) {
	if ev.sessionID == "" {
		ev.sessionID = id
	}
}

// parser records one event line in ev and reports whether it was a usable
// event of the agent's format.
type parser func(ev *events, line []byte) bool

// readEvents reads r until EOF, a read error, or the first line longer than
// maxEventSize. It holds at most one line in memory. A line that is not JSON
// is recorded but does not stop parsing, so a later session id is still kept.
func readEvents(r io.Reader, parse parser) events {
	var ev events
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxEventSize+1)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if !parse(&ev, line) {
			ev.notJSON = true
		}
	}
	ev.tooLarge = errors.Is(sc.Err(), bufio.ErrTooLong)
	return ev
}
```

Create `internal/shell/claude.go`:

```go
package shell

import "encoding/json"

// claudeHeader holds the fields aits reads from every Claude Code event.
// Decoding into it skips all other fields without copying them, however large
// they are.
type claudeHeader struct {
	Type      json.RawMessage `json:"type"`
	SessionID json.RawMessage `json:"session_id"`
}

type claudeResult struct {
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
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
	ev.done = true
	if res.IsError {
		ev.failed, ev.failure = true, res.Result
	} else {
		ev.answer = res.Result
	}
	return true
}
```

In `internal/shell/agent.go`, add the field after `envPrefix`:

```go
	parse      parser   // turns one line of the agent's output into events
```

and `parse: parseClaude,` to the `"claude"` entry.

In `internal/shell/process.go`, change `supervise`'s signature and reader goroutine, and its call in `run`:

```go
	return supervise(ctx, cmd, outR, errR, req.Timeout, ag.parse), nil
```

```go
func supervise(ctx context.Context, cmd *exec.Cmd, out, errOut *os.File, timeout time.Duration, parse parser) observation {
```

```go
	go func() { evCh <- readEvents(out, parse) }()
```

- [ ] **Step 6: Implement the outcome on the neutral record**

In `internal/shell/outcome.go`, replace `decide` and add `failureMessage`:

```go
// decide maps an observation to a status and a one-line message, checking the
// conditions in the order of the #2 spec §6.
func decide(o observation) (Status, string) {
	ev := o.events
	switch {
	case o.stopped:
		return StatusStopped, "aits was stopped by a signal before the agent finished"
	case o.timedOut:
		return StatusTimeout, "the agent did not finish before the timeout"
	case ev.tooLarge:
		return StatusTooLarge, fmt.Sprintf("an agent output event exceeded %d MiB", maxEventSize>>20)
	case o.exitCode != 0:
		return StatusAgentFailed, failureMessage(ev, o.exitCode)
	case ev.notJSON:
		return StatusBadOutput, "the agent printed a line that is not a usable JSON event"
	case ev.failed:
		return StatusAgentFailed, failureMessage(ev, o.exitCode)
	case !ev.done:
		return StatusBadOutput, "the agent output has no final event"
	case ev.answer == "":
		return StatusBadOutput, "the agent's answer is empty"
	case ev.sessionID == "":
		return StatusBadOutput, "the agent output has no session id"
	}
	return StatusOK, ""
}

// failureMessage prefers the error the agent reported over its exit code,
// which alone hides the reason.
func failureMessage(ev events, exitCode int) string {
	switch {
	case ev.failure != "":
		return "the agent reported an error: " + firstLine(ev.failure, 200)
	case exitCode != 0:
		return fmt.Sprintf("the agent exited with code %d", exitCode)
	}
	return "the agent reported an error"
}
```

In `internal/shell/shell.go` `Submit`, replace `res.Answer = obs.events.result.Result` with `res.Answer = obs.events.answer`.

- [ ] **Step 7: Run the tests to see them pass**

Run: `gofmt -l . ; go vet ./... && go test -count=1 ./... 2>&1 | tail -5`
Expected: no gofmt output; all four packages `ok`.

- [ ] **Step 8: Commit**

```bash
REF='#13' commit.sh "refactor(shell): vendor-neutral event record; report the agent's own error" \
  internal/shell/events.go internal/shell/claude.go internal/shell/agent.go internal/shell/outcome.go \
  internal/shell/shell.go internal/shell/process.go internal/shell/events_test.go internal/shell/outcome_test.go \
  internal/shell/testdata/fakeclaude/main.go cmd/aits/e2e_test.go
```

---

### Task 2: Codex agent and parser

**Files:**
- Create: `internal/shell/codex.go`, `internal/shell/codex_test.go`
- Modify: `internal/shell/agent.go` (base allowlist, Codex entry, `environ`)
- Modify: `internal/shell/agent_test.go`, `internal/shell/shell_test.go`
- Modify: `docs/cli.md` (regenerated)

**Interfaces:**
- Consumes: `events`, `parser`, `readEvents` (Task 1).
- Produces: `func parseCodex(ev *events, line []byte) bool`; `agents["codex"]`; `var baseEnv []string`; `Agents()` returns `["claude" "codex"]`.

- [ ] **Step 1: Write the Codex parser tests**

Create `internal/shell/codex_test.go`:

```go
package shell

import (
	"strings"
	"testing"
)

func TestParseCodex(t *testing.T) {
	const (
		started   = `{"type":"thread.started","thread_id":"t1"}`
		turn      = `{"type":"turn.started"}`
		message   = `{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"the answer"}}`
		completed = `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`
	)
	lines := func(l ...string) string { return strings.Join(l, "\n") + "\n" }
	ok := events{sessionID: "t1", answer: "the answer", done: true}
	tests := []struct {
		name string
		in   string
		want events
	}{
		{"turn", lines(started, turn, message, completed), ok},
		{"last message wins", lines(started, turn,
			`{"type":"item.completed","item":{"type":"agent_message","text":"first"}}`,
			`{"type":"item.completed","item":{"type":"command_execution","command":"ls","aggregated_output":"x","exit_code":0}}`,
			message, completed), ok},
		{"large tool output", lines(started, turn,
			`{"type":"item.completed","item":{"type":"command_execution","aggregated_output":"`+strings.Repeat("x", 1<<20)+`"}}`,
			message, completed), ok},
		{"retry notices ignored", lines(started, turn, `{"type":"error","message":"Reconnecting... 1/5"}`, message, completed), ok},
		{"unknown event types", lines(started, turn, `{"type":"turn.diff","diff":"--- a"}`, message, completed), ok},
		{"first thread id wins", lines(started, `{"type":"thread.started","thread_id":"t2"}`, turn, message, completed), ok},
		{"turn failed", lines(started, turn, `{"type":"error","message":"retry"}`, `{"type":"turn.failed","error":{"message":"401 Unauthorized"}}`),
			events{sessionID: "t1", done: true, failed: true, failure: "401 Unauthorized"}},
		{"turn failed without message", lines(started, turn, `{"type":"turn.failed"}`),
			events{sessionID: "t1", done: true, failed: true}},
		{"no turn.completed", lines(started, turn, message), events{sessionID: "t1", answer: "the answer"}},
		{"no thread id", lines(turn, message, completed), events{answer: "the answer", done: true}},
		{"not json first", lines("hello", started, turn, message, completed),
			events{sessionID: "t1", answer: "the answer", done: true, notJSON: true}},
		{"json null", "null\n", events{notJSON: true}},
		{"message text of wrong type", `{"type":"item.completed","item":{"type":"agent_message","text":42}}`, events{notJSON: true}},
		{"thread id of wrong type", `{"type":"thread.started","thread_id":7}`, events{notJSON: true}},
		{"other item with odd fields", `{"type":"item.completed","item":{"type":"reasoning","text":42}}`, events{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := readEvents(strings.NewReader(tt.in), parseCodex); got != tt.want {
				t.Errorf("readEvents = %+v, want %+v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Write the agent table tests**

Replace `internal/shell/agent_test.go` with:

```go
package shell

import (
	"slices"
	"testing"
)

func TestEnviron(t *testing.T) {
	parent := []string{
		"PATH=/bin", "HOME=/h", "LC_ALL=C",
		"ANTHROPIC_API_KEY=a", "CLAUDE_CONFIG_DIR=/c", "OPENAI_API_KEY=o", "CODEX_HOME=/x",
		"AITS_CANARY=secret", "GITHUB_TOKEN=t", "EXTRA=e", "NOEQUALS",
	}
	want := map[string][]string{
		"claude": {"PATH=/bin", "HOME=/h", "LC_ALL=C", "ANTHROPIC_API_KEY=a", "CLAUDE_CONFIG_DIR=/c", "EXTRA=e"},
		"codex":  {"PATH=/bin", "HOME=/h", "LC_ALL=C", "OPENAI_API_KEY=o", "CODEX_HOME=/x", "EXTRA=e"},
	}
	for name, w := range want {
		if got := agents[name].environ(parent, []string{"EXTRA"}); !slices.Equal(got, w) {
			t.Errorf("%s environ = %q, want %q", name, got, w)
		}
	}
}

func TestAgents(t *testing.T) {
	if got := Agents(); !slices.Equal(got, []string{"claude", "codex"}) {
		t.Errorf("Agents() = %q", got)
	}
	for name, ag := range agents {
		if ag.name != name || ag.parse == nil || slices.Contains(ag.args, "--skip-git-repo-check") {
			t.Errorf("agent %q is misconfigured: %+v", name, ag)
		}
	}
}
```

In `internal/shell/shell_test.go` `TestRequestValidate`, after the `good` request check add:

```go
	codex := good
	codex.Agent = "codex"
	if _, err := codex.validate(); err != nil {
		t.Fatalf("valid codex request rejected: %v", err)
	}
```

- [ ] **Step 3: Run the tests to see them fail**

Run: `go test -count=1 ./internal/shell 2>&1 | tail -5`
Expected: build failure `undefined: parseCodex`.

- [ ] **Step 4: Implement the Codex parser**

Create `internal/shell/codex.go`:

```go
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
```

- [ ] **Step 5: Add Codex to the agent table with the shared base allowlist**

In `internal/shell/agent.go`, replace everything from the `agent` type through `environ` with:

```go
// agent describes how to run one vendor CLI.
type agent struct {
	name       string
	executable string
	args       []string // never carries the prompt (P8)
	env        []string // the agent's own variables, passed on top of baseEnv when set
	parse      parser   // turns one line of the agent's output into events
}

// baseEnv is passed to every agent when set, together with every LC_*
// variable (P8).
var baseEnv = []string{
	"PATH", "HOME", "USER", "LOGNAME", "LANG", "TZ", "TMPDIR",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
}

var agents = map[string]agent{
	"claude": {
		name:       "claude",
		executable: "claude",
		args:       []string{"-p", "--output-format", "stream-json", "--verbose"},
		env:        []string{"CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY"},
		parse:      parseClaude,
	},
	"codex": {
		name:       "codex",
		executable: "codex",
		// No --skip-git-repo-check, sandbox or approval flags: Codex keeps its
		// own safety defaults (P9).
		args:  []string{"exec", "--json", "-"},
		env:   []string{"CODEX_HOME", "OPENAI_API_KEY"},
		parse: parseCodex,
	},
}

// Agents returns the supported agent names, sorted.
func Agents() []string { return slices.Sorted(maps.Keys(agents)) }

// environ builds the child's environment from scratch (P8): the base
// allowlist, the agent's own variables and the names in pass, with values
// taken from parent.
func (a agent) environ(parent, pass []string) []string {
	var out []string
	for _, kv := range parent {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if slices.Contains(baseEnv, name) || strings.HasPrefix(name, "LC_") ||
			slices.Contains(a.env, name) || slices.Contains(pass, name) {
			out = append(out, kv)
		}
	}
	return out
}
```

- [ ] **Step 6: Run the unit tests, then regenerate the CLI reference**

Run: `go test -count=1 ./internal/shell 2>&1 | tail -3`
Expected: `ok`.

Run: `go test -count=1 ./cmd/aits -run TestCLIDocs 2>&1 | tail -2`
Expected: FAIL `docs/cli.md is out of date` (the `--agent` line now lists `claude, codex`).

Run: `go test -count=1 ./cmd/aits -run TestCLIDocs -update && grep -n "One of" docs/cli.md`
Expected: `--agent AGENT       AGENT CLI to run (required). One of: claude, codex.`

- [ ] **Step 7: Run everything**

Run: `gofmt -l . ; go vet ./... && go test -count=1 ./... 2>&1 | tail -5`
Expected: all `ok`.

- [ ] **Step 8: Commit**

```bash
REF='#13' commit.sh "feat(shell): add the codex agent" \
  internal/shell/codex.go internal/shell/codex_test.go internal/shell/agent.go \
  internal/shell/agent_test.go internal/shell/shell_test.go docs/cli.md
```

---

### Task 3: Fake vendor dialects and per-agent functional tests

**Files:**
- Move: `internal/shell/testdata/fakeclaude/` → `internal/shell/testdata/fakevendor/`
- Modify: `internal/shell/testdata/fakevendor/main.go` (whole file)
- Modify: `cmd/aits/e2e_test.go`, `cmd/aits/lifecycle_test.go`
- Create: `cmd/aits/codex_test.go`

**Interfaces:**
- Consumes: `agents["codex"]` (Task 2); scenario names `agent_error`, `agent_error_exit0` (Task 1).
- Produces (test helpers in package `main`): `var agentNames = []string{"claude", "codex"}`; `func vendorAs(t *testing.T, agent, scenario string) string`; `func agentArgs(agent string, extra ...string) []string`; `var agentArgv map[string][]string`. `vendor(t, scenario)` and `submitArgs(extra...)` remain as Claude shorthands.

- [ ] **Step 1: Move the fake and point the harness at it (no behaviour change)**

Run: `git mv internal/shell/testdata/fakeclaude internal/shell/testdata/fakevendor`

In `cmd/aits/e2e_test.go`:
- comment of `fakeBin`: `// the fake vendor executable`
- `fakeBin = filepath.Join(dir, "fakevendor")`
- build map key `"../../internal/shell/testdata/fakevendor": fakeBin`

Run: `go test -count=1 ./cmd/aits 2>&1 | tail -2`
Expected: `ok`.

- [ ] **Step 2: Add the per-agent helpers**

In `cmd/aits/e2e_test.go`, replace `vendor` and `submitArgs` with:

```go
// agentNames are the agents every shared scenario runs against.
var agentNames = []string{"claude", "codex"}

// agentArgv is the argv each agent must be started with; the prompt is never
// in it (P8).
var agentArgv = map[string][]string{
	"claude": {"-p", "--output-format", "stream-json", "--verbose"},
	"codex":  {"exec", "--json", "-"},
}

// vendor installs the fake vendor as claude, set to scenario.
func vendor(t *testing.T, scenario string) string {
	t.Helper()
	return vendorAs(t, "claude", scenario)
}

// vendorAs installs the fake vendor under the name agent, set to scenario,
// in a fresh directory and returns the directory, for use as PATH. The fake
// speaks the dialect its executable name says.
func vendorAs(t *testing.T, agent, scenario string) string {
	t.Helper()
	dir := t.TempDir()
	// A hard link, not a symlink: os.Executable resolves symlinks, and the fake
	// must find its scenario file and its name in this directory.
	if err := os.Link(fakeBin, filepath.Join(dir, agent)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scenario"), []byte(scenario), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func submitArgs(extra ...string) []string { return agentArgs("claude", extra...) }

func agentArgs(agent string, extra ...string) []string {
	return append([]string{"shell", "submit", "--agent", agent}, extra...)
}
```

- [ ] **Step 3: Make the shared functional tests run per agent**

In `cmd/aits/e2e_test.go`, replace `TestSubmitSuccess`, `TestSubmitAgentNotFound`, `TestSubmitEnvironment`, `TestSubmitAgentFailed`, `TestSubmitAgentReportsError` and `TestSubmitBadOutput` with:

```go
func TestSubmitSuccess(t *testing.T) {
	for _, agent := range agentNames {
		t.Run(agent, func(t *testing.T) {
			bin, work := vendorAs(t, agent, "ok"), t.TempDir()
			out := call{path: bin, args: agentArgs(agent, "--cwd", work, "--json", "Summarize this repo")}.run(t)
			if out.code != 0 {
				t.Fatalf("exit %d, stdout %q, stderr %q", out.code, out.stdout, out.stderr)
			}
			r := decode(t, out.stdout)
			if r.Status != "ok" || r.Agent != agent || r.SessionID != "sess-fake-1" || r.Answer != "fake answer" ||
				r.ExitCode == nil || *r.ExitCode != 0 || r.DurationMS == nil || r.Message != "" || r.Error != nil {
				t.Fatalf("result = %+v", r)
			}
			rec := readRecord(t, bin)
			if rec.Stdin != "Summarize this repo" {
				t.Errorf("agent stdin = %q", rec.Stdin)
			}
			if want := agentArgv[agent]; !slices.Equal(rec.Args, want) {
				t.Errorf("agent argv = %q, want %q (the prompt must not be in argv)", rec.Args, want)
			}
			wantDir, _ := filepath.EvalSymlinks(work)
			if gotDir, _ := filepath.EvalSymlinks(rec.Dir); gotDir != wantDir {
				t.Errorf("agent dir = %q, want %q", gotDir, wantDir)
			}
		})
	}
}
```

```go
func TestSubmitAgentNotFound(t *testing.T) {
	for _, agent := range agentNames {
		t.Run(agent, func(t *testing.T) {
			out := call{args: agentArgs(agent, "--json", "hi")}.run(t)
			if r := decode(t, out.stdout); out.code != 1 || r.Error == nil || r.Error.Code != "agent_not_found" {
				t.Fatalf("code=%d stdout=%q", out.code, out.stdout)
			}
		})
	}
}
```

```go
func TestSubmitEnvironment(t *testing.T) {
	own := map[string][]string{
		"claude": {"ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR"},
		"codex":  {"OPENAI_API_KEY", "CODEX_HOME"},
	}
	for _, agent := range agentNames {
		t.Run(agent, func(t *testing.T) {
			bin := vendorAs(t, agent, "ok")
			env := []string{"AITS_TEST_CANARY=secret-canary", "AITS_TEST_PASSED=passed-value"}
			for _, names := range own {
				for _, n := range names {
					env = append(env, n+"=v-"+n)
				}
			}
			out := call{path: bin, env: env, args: agentArgs(agent, "--env", "AITS_TEST_PASSED", "--json", "hi")}.run(t)
			if out.code != 0 {
				t.Fatalf("exit %d: %s", out.code, out.stdout)
			}
			got := readRecord(t, bin).Env
			if !slices.Contains(got, "AITS_TEST_PASSED=passed-value") {
				t.Errorf("--env variable missing from %q", got)
			}
			for _, kv := range got {
				if strings.HasPrefix(kv, "AITS_TEST_CANARY=") || strings.HasPrefix(kv, "AITS_HOME=") {
					t.Errorf("variable outside the allowlist reached the agent: %s", kv)
				}
			}
			if !slices.ContainsFunc(got, func(kv string) bool { return strings.HasPrefix(kv, "HOME=") }) {
				t.Errorf("allowlisted HOME missing from %q", got)
			}
			for owner, names := range own {
				for _, n := range names {
					if has := slices.Contains(got, n+"=v-"+n); has != (owner == agent) {
						t.Errorf("%s passed to %s: %v, want %v", n, agent, has, owner == agent)
					}
				}
			}
		})
	}
}
```

```go
func TestSubmitAgentFailed(t *testing.T) {
	for _, agent := range agentNames {
		t.Run(agent, func(t *testing.T) {
			out := call{path: vendorAs(t, agent, "exit_nonzero"), args: agentArgs(agent, "--json", "hi")}.run(t)
			r := decode(t, out.stdout)
			if out.code != 1 || r.Status != "agent_failed" || r.ExitCode == nil || *r.ExitCode != 3 ||
				r.SessionID != "sess-fake-1" || r.Answer != "" || r.Message != "the agent exited with code 3" {
				t.Fatalf("code=%d result=%+v", out.code, r)
			}
			if !strings.HasSuffix(r.StderrTail, "boom\n") || len(r.StderrTail) > 4096 {
				t.Errorf("stderr_tail: %d bytes ending %q", len(r.StderrTail), r.StderrTail[max(0, len(r.StderrTail)-10):])
			}
		})
	}
}

func TestSubmitAgentReportsError(t *testing.T) {
	for _, agent := range agentNames {
		for scenario, exit := range map[string]int{"agent_error": 1, "agent_error_exit0": 0} {
			t.Run(agent+"/"+scenario, func(t *testing.T) {
				out := call{path: vendorAs(t, agent, scenario), args: agentArgs(agent, "--json", "hi")}.run(t)
				r := decode(t, out.stdout)
				if out.code != 1 || r.Status != "agent_failed" || r.ExitCode == nil || *r.ExitCode != exit ||
					r.SessionID != "sess-fake-1" || r.Answer != "" || r.Message != "the agent reported an error: fake failure" {
					t.Fatalf("code=%d result=%+v", out.code, r)
				}
			})
		}
	}
}

func TestSubmitBadOutput(t *testing.T) {
	for _, agent := range agentNames {
		for scenario, session := range map[string]string{
			"not_json":     "sess-fake-1", // the session id arrives after the bad line
			"no_result":    "sess-fake-1",
			"empty_result": "sess-fake-1",
			"no_session":   "",
		} {
			t.Run(agent+"/"+scenario, func(t *testing.T) {
				out := call{path: vendorAs(t, agent, scenario), args: agentArgs(agent, "--json", "hi")}.run(t)
				r := decode(t, out.stdout)
				if out.code != 1 || r.Status != "bad_agent_output" || r.SessionID != session || r.Answer != "" || r.Message == "" {
					t.Fatalf("code=%d result=%+v, want session_id %q", out.code, r, session)
				}
			})
		}
	}
}
```

- [ ] **Step 4: Make the lifecycle tests run per agent**

In `cmd/aits/lifecycle_test.go`, wrap the bodies of `TestSubmitTimeout`, `TestSubmitTimeoutEscalatesToKill`, `TestSubmitOutputTooLarge`, `TestSubmitLingeringGrandchild` and `TestSubmitUnreadPrompt` in

```go
	for _, agent := range agentNames {
		t.Run(agent, func(t *testing.T) {
			// existing body, with vendor(t, X) → vendorAs(t, agent, X)
			// and submitArgs(...) → agentArgs(agent, ...)
		})
	}
```

and change `TestSubmitStopped` to loop over both:

```go
func TestSubmitStopped(t *testing.T) {
	for _, agent := range agentNames {
		for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
			t.Run(agent+"/"+sig.String(), func(t *testing.T) {
				bin := vendorAs(t, agent, "sleep")
				cmd, stdout, stderr := call{path: bin, args: agentArgs(agent, "--json", "hi")}.command(t)
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				waitFile(t, filepath.Join(bin, "child.pid"))
				if err := cmd.Process.Signal(sig); err != nil {
					t.Fatal(err)
				}
				out := finish(t, cmd.Wait(), stdout, stderr)
				r := decode(t, out.stdout)
				if out.code != 1 || r.Status != "stopped" || r.SessionID != "sess-fake-1" {
					t.Fatalf("code=%d result=%+v", out.code, r)
				}
				assertGone(t, readPID(t, filepath.Join(bin, "child.pid")))
				assertGone(t, readPID(t, filepath.Join(bin, "grandchild.pid")))
			})
		}
	}
}
```

`TestSubmitStoppedWhileReadingPrompt` stays Claude-only: nothing is started, so the agent does not matter.

- [ ] **Step 5: Write the Codex-only functional tests**

Create `cmd/aits/codex_test.go`:

```go
package main

import (
	"slices"
	"strings"
	"testing"
)

// The answer is the last agent_message of the turn; earlier ones are progress.
func TestCodexAnswerIsLastMessage(t *testing.T) {
	out := call{path: vendorAs(t, "codex", "several_messages"), args: agentArgs("codex", "--json", "hi")}.run(t)
	if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" || r.Answer != "fake answer" {
		t.Fatalf("code=%d result=%+v", out.code, r)
	}
}

// Codex sends "error" events while it retries; they fail nothing on their own.
func TestCodexRetryNoticesAreNotFailures(t *testing.T) {
	out := call{path: vendorAs(t, "codex", "retry_errors"), args: agentArgs("codex", "--json", "hi")}.run(t)
	if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" || r.Answer != "fake answer" {
		t.Fatalf("code=%d result=%+v", out.code, r)
	}
}

// aits keeps Codex's refusal to run outside a git repository (P9).
func TestCodexKeepsGitRepoCheck(t *testing.T) {
	bin := vendorAs(t, "codex", "not_git")
	out := call{path: bin, args: agentArgs("codex", "--json", "hi")}.run(t)
	r := decode(t, out.stdout)
	if out.code != 1 || r.Status != "agent_failed" || r.ExitCode == nil || *r.ExitCode != 1 ||
		!strings.Contains(r.StderrTail, "--skip-git-repo-check was not specified") {
		t.Fatalf("code=%d result=%+v", out.code, r)
	}
	if args := readRecord(t, bin).Args; slices.Contains(args, "--skip-git-repo-check") {
		t.Errorf("aits passed --skip-git-repo-check: %q", args)
	}
}
```

- [ ] **Step 6: Run the tests to see the Codex ones fail**

Run: `go test -count=1 ./cmd/aits 2>&1 | grep -E "^(---|ok|FAIL)" | head -30`
Expected: every `.../codex...` subtest and the three `TestCodex*` tests FAIL (the fake still speaks Claude, so Codex runs end `bad_agent_output` or the scenario is unknown); every `.../claude...` subtest passes.

- [ ] **Step 7: Rewrite the fake vendor with dialects**

Replace `internal/shell/testdata/fakevendor/main.go` with:

```go
// Command fakevendor stands in for the vendor agent CLIs in tests (P12).
//
// It speaks the dialect named by its executable's base name, "claude" or
// "codex". It reads a scenario name from the file "scenario" next to its
// executable, records what it was given to "record.json" in the same
// directory, and then behaves as the scenario says. The scenario comes from a
// file, not the environment, because aits filters the environment it passes
// on (P8).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const sessionID = "sess-fake-1"

type record struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
	Dir   string   `json:"dir"`
	Env   []string `json:"env"`
}

// dialect writes one vendor's events.
type dialect struct {
	start     func()            // the events that open a run, carrying the session id
	progress  func()            // an event that is neither the session id nor the answer
	answer    func(text string) // a reply and the end of the run
	fail      func(text string) // a reported error and the end of the run
	noSession func(text string) // a reply and the end of the run, without any session id
}

var dialects = map[string]dialect{
	"claude": {
		start: func() { emit(map[string]any{"type": "system", "subtype": "init", "session_id": sessionID}) },
		progress: func() {
			emit(map[string]any{"type": "assistant", "session_id": sessionID, "message": map[string]any{"content": []any{}}})
		},
		answer:    func(text string) { emit(claudeResult(false, text, sessionID)) },
		fail:      func(text string) { emit(claudeResult(true, text, sessionID)) },
		noSession: func(text string) { emit(claudeResult(false, text, "")) },
	},
	"codex": {
		start: func() {
			emit(map[string]any{"type": "thread.started", "thread_id": sessionID})
			emit(map[string]any{"type": "turn.started"})
		},
		progress: func() {
			emit(codexItem(map[string]any{"type": "command_execution", "command": "ls", "aggregated_output": "README.md\n", "exit_code": 0}))
		},
		answer: codexAnswer,
		fail: func(text string) {
			emit(map[string]any{"type": "error", "message": "Reconnecting... 1/5"})
			emit(map[string]any{"type": "turn.failed", "error": map[string]any{"message": text}})
		},
		noSession: func(text string) {
			emit(map[string]any{"type": "turn.started"})
			codexAnswer(text)
		},
	},
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "--fake-grandchild" {
		writePID(filepath.Join(os.Args[2], "grandchild.pid"))
		time.Sleep(10 * time.Minute)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		fail(err)
	}
	d, ok := dialects[filepath.Base(exe)]
	if !ok {
		fail(fmt.Errorf("no dialect for executable name %q", filepath.Base(exe)))
	}
	dir := filepath.Dir(exe)
	scenario, err := os.ReadFile(filepath.Join(dir, "scenario"))
	if err != nil {
		fail(err)
	}
	name := strings.TrimSpace(string(scenario))
	var stdin []byte
	if name != "ignore_stdin" {
		if stdin, err = io.ReadAll(os.Stdin); err != nil {
			fail(err)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	rec, err := json.Marshal(record{Args: os.Args[1:], Stdin: string(stdin), Dir: wd, Env: os.Environ()})
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "record.json"), rec, 0o600); err != nil {
		fail(err)
	}
	play(d, name, dir, exe)
}

func play(d dialect, scenario, dir, exe string) {
	switch scenario {
	case "ok":
		d.start()
		d.progress()
		d.answer("fake answer")
	case "exit_nonzero":
		d.start()
		os.Stderr.Write(bytes.Repeat([]byte("x"), 100<<10))
		os.Stderr.WriteString("\nboom\n")
		os.Exit(3)
	case "agent_error":
		// What both CLIs do when not logged in: report the error, exit 1.
		d.start()
		d.fail("fake failure\nsecond line")
		os.Exit(1)
	case "agent_error_exit0":
		d.start()
		d.fail("fake failure\nsecond line")
	case "not_json":
		fmt.Println("this is not json")
		d.start()
		d.answer("fake answer")
	case "no_result":
		d.start()
	case "empty_result":
		d.start()
		d.answer("")
	case "no_session":
		d.noSession("fake answer")
	case "huge_line":
		d.start()
		os.Stdout.Write(bytes.Repeat([]byte("x"), 16<<20+1))
		time.Sleep(10 * time.Minute)
	case "sleep":
		d.start()
		spawnGrandchild(dir, exe)
		writePID(filepath.Join(dir, "child.pid"))
		time.Sleep(10 * time.Minute)
	case "ignore_term":
		signal.Ignore(syscall.SIGTERM)
		d.start()
		writePID(filepath.Join(dir, "child.pid"))
		time.Sleep(10 * time.Minute)
	case "ignore_stdin":
		// Never reads the prompt; a grandchild keeps stdin (and stdout) open.
		d.start()
		d.answer("fake answer")
		spawnGrandchild(dir, exe)
	case "linger":
		d.start()
		d.answer("fake answer")
		spawnGrandchild(dir, exe)
	case "several_messages": // Codex only
		d.start()
		emit(codexItem(map[string]any{"type": "agent_message", "text": "Let me look."}))
		d.progress()
		d.answer("fake answer")
	case "retry_errors": // Codex only
		d.start()
		emit(map[string]any{"type": "error", "message": "Reconnecting... 1/5"})
		emit(map[string]any{"type": "error", "message": "Reconnecting... 2/5"})
		d.answer("fake answer")
	case "not_git": // Codex only
		os.Stderr.WriteString("Not inside a trusted directory and --skip-git-repo-check was not specified.\n")
		os.Exit(1)
	default:
		fail(fmt.Errorf("unknown scenario %q", scenario))
	}
}

func claudeResult(isError bool, text, session string) map[string]any {
	ev := map[string]any{"type": "result", "subtype": "success", "is_error": isError, "result": text}
	if session != "" {
		ev["session_id"] = session
	}
	return ev
}

func codexItem(item map[string]any) map[string]any {
	return map[string]any{"type": "item.completed", "item": item}
}

func codexAnswer(text string) {
	emit(codexItem(map[string]any{"type": "agent_message", "text": text}))
	emit(map[string]any{"type": "turn.completed", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
}

func emit(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fail(err)
	}
	os.Stdout.Write(append(b, '\n'))
}

// spawnGrandchild starts a process in the same group that keeps the agent's
// stdout and stderr open, and waits until it has written its pid.
func spawnGrandchild(dir, exe string) {
	cmd := exec.Command(exe, "--fake-grandchild", dir)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		fail(err)
	}
	pidFile := filepath.Join(dir, "grandchild.pid")
	for i := 0; i < 500; i++ {
		if _, err := os.Stat(pidFile); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	fail(fmt.Errorf("grandchild did not start"))
}

// writePID writes this process's pid to path atomically.
func writePID(path string) {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		fail(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "fakevendor:", err)
	os.Exit(97)
}
```

- [ ] **Step 8: Run everything**

Run: `gofmt -l . ; go vet ./... && go test -count=1 ./... 2>&1 | tail -5`
Expected: all four packages `ok`; `cmd/aits` takes longer than before (lifecycle tests run twice).

- [ ] **Step 9: Commit**

```bash
REF='#13' commit.sh "test: fake vendor speaks claude and codex; run shared scenarios for both" \
  internal/shell/testdata cmd/aits/e2e_test.go cmd/aits/lifecycle_test.go cmd/aits/codex_test.go
```

(`git mv` already staged the rename; `commit.sh` adds the rest.)

---

### Task 4: Docs and live test

**Files:**
- Modify: `internal/cli/shell.go` (`submitDetails`, `bad_agent_output` row)
- Modify: `docs/cli.md` (regenerated)
- Modify: `cmd/aits/live_test.go`
- Modify: `README.md` (Usage), `CONTRIBUTING.md` (live test line)
- Modify: `docs/superpowers/specs/2026-10-13-shell-submit-design.md` (§5, §6 pointers)

**Interfaces:**
- Consumes: everything above.
- Produces: `TestLiveCodex`, `liveSubmit(t *testing.T, agent, dir string)`.

- [ ] **Step 1: Write the live tests**

Replace `cmd/aits/live_test.go` with:

```go
//go:build live

package main

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
)

// The live tests run the real vendor CLIs (P12: opt-in, never in CI). They
// need a logged-in CLI on PATH and the real HOME, where its credentials live;
// AITS_HOME stays temporary. Each spends one model call.
//
//	go test -tags live ./cmd/aits -run TestLive

func TestLiveClaude(t *testing.T) { liveSubmit(t, "claude", t.TempDir()) }

// Codex refuses to run outside a git repository, and aits keeps that check.
func TestLiveCodex(t *testing.T) {
	work := t.TempDir()
	if b, err := exec.Command("git", "init", "-q", work).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, b)
	}
	liveSubmit(t, "codex", work)
}

func liveSubmit(t *testing.T, agent, dir string) {
	t.Helper()
	cmd := exec.Command(aitsBin, "shell", "submit", "--agent", agent, "--cwd", dir,
		"--timeout", "5m", "--json", "Reply with exactly: OK")
	cmd.Env = append(os.Environ(), "AITS_HOME="+t.TempDir())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	out := finish(t, cmd.Run(), &stdout, &stderr)
	r := decode(t, out.stdout)
	if out.code != 0 || r.Status != "ok" || r.SessionID == "" || r.Answer == "" {
		t.Fatalf("code=%d result=%+v stderr=%q", out.code, r, out.stderr)
	}
}
```

Run: `go vet -tags live ./cmd/aits && go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 -tags live ./...`
Expected: no output.

Run (spends one Codex call): `go test -count=1 -tags live ./cmd/aits -run TestLiveCodex -v 2>&1 | tail -3`
Expected: `--- PASS: TestLiveCodex`.

- [ ] **Step 2: Update the help text and regenerate the reference**

In `internal/cli/shell.go` `submitDetails`, replace the line

```
  bad_agent_output  The agent's output was not usable stream-json.
```

with

```
  bad_agent_output  The agent's JSON events were not usable.
```

Run: `go test -count=1 ./cmd/aits -run 'TestCLIDocs|TestSubmitHelpDocumentsOutput' -update; go test -count=1 ./cmd/aits -run 'TestCLIDocs|TestSubmitHelpDocumentsOutput'`
Expected: `ok`; `git diff docs/cli.md` shows only the `bad_agent_output` line.

- [ ] **Step 3: Update README, CONTRIBUTING and the #2 spec**

In `README.md`, after the existing usage block

```
    aits shell submit --agent claude --cwd ~/src/proj --json "Summarize what this repo does"
```

add

```

`--agent codex` runs Codex CLI the same way; Codex only runs inside a git
repository.
```

In `CONTRIBUTING.md`, replace

```
- `go test -tags live ./cmd/aits -run TestLive` runs one real Claude Code call
  with your own login.
```

with

```
- `go test -tags live ./cmd/aits -run TestLive` runs one real call per agent
  (Claude Code, Codex) with your own logins; `-run TestLiveCodex` runs one.
```

In `docs/superpowers/specs/2026-10-13-shell-submit-design.md`, append to the end of §5 (after the "Reading stderr" paragraph):

```

Since #13 the event record is vendor-neutral and each agent has its own
parser; see `2026-10-14-shell-codex-design.md` §5.
```

and append to the end of §6 (after the "Without `--json`" paragraph):

```

Since #13, `message` for `agent_failed` carries the error the agent reported
when there is one, even when it exited non-zero; see
`2026-10-14-shell-codex-design.md` §5.
```

- [ ] **Step 4: Run everything**

Run: `gofmt -l . ; go vet ./... && go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 -tags live ./... && go test -count=1 ./... 2>&1 | tail -5`
Expected: all `ok`, no other output.

- [ ] **Step 5: Commit**

```bash
REF='#13' commit.sh "docs: codex in help, README and specs; live test for codex" \
  internal/cli/shell.go docs/cli.md cmd/aits/live_test.go README.md CONTRIBUTING.md \
  docs/superpowers/specs/2026-10-13-shell-submit-design.md
```
