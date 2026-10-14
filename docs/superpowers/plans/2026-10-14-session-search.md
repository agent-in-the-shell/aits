# `aits session search` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `aits session search TERM...` finds Claude Code and Codex conversations whose message text contains every term and returns a `session_id` that `aits shell submit --session` continues.

**Architecture:** New package `internal/session`, independent of `internal/shell` (P1). Per vendor, a fixed `filepath.Glob` lists transcript files; a pool of workers prefilters each file in 1 MiB chunks on lower-cased bytes, then parses candidate files line by line, feeding only user/assistant message text into a per-file `transcript` matcher. Results are sorted by file mtime. A new CLI group `session` with leaf `search` prints the `Output` as JSON or text.

**Tech Stack:** Go 1.26 standard library only.

**Spec:** `docs/superpowers/specs/2026-10-14-session-search-design.md`. Issue: #17.

## Global Constraints

- Standard library only, no new `require` (P3); `CGO_ENABLED=0`; Linux and macOS.
- `gofmt -l .` empty; `go vet ./...` and `go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 -tags live ./...` clean.
- `internal/session` never imports `internal/shell` (P1).
- Never write under the vendor roots or `AITS_HOME`; works with a read-only `HOME` (P7, P12).
- Searched text: Claude `user`/`assistant` events that are not `isSidechain`/`isMeta`, string content or `text` blocks, minus user text starting with `<task-notification>`, `<local-command-`, `<command-`, `<system-reminder>`; Codex `response_item` `message` with role `user`/`assistant`, `input_text`/`output_text` blocks, minus user text starting with `<environment_context>`.
- Globs: Claude `projects/*/*.jsonl` under `$CLAUDE_CONFIG_DIR` (default `$HOME/.claude`); Codex `sessions/*/*/*/rollout-*.jsonl` under `$CODEX_HOME` (default `$HOME/.codex`).
- Snippet ≤ 160 bytes plus `…` marks; line limit 64 MiB; chunk 1 MiB; default limit 20.
- No real transcript content in the repo: fixtures are written by tests.
- Code, docs and commits in English; every commit leak-checked, body `Refs #17`.

## Review Focus

1. A term split across the 1 MiB chunk boundary → still found — pinned by `TestMayMatch/split across chunks` (Task 2).
2. A vendor root path containing glob metacharacters (`[`, `*`) → files still listed, not "no results" — pinned by `TestFilesEscapesRoot` (Task 2).
3. A term containing `"` or `\` (escaped in JSON) → still found through the parse step — pinned by `TestSearch/quote in term` (Task 2).
4. Ctrl-C during a multi-second search → stops promptly with the `stopped` error, not after the whole scan — pinned by `TestSearchCancelled` (Task 2).
5. Event lines of shapes the parser does not know (other `type`s, `message` absent) → ignored, not counted as skipped — pinned by `TestParseClaude/other event types` and `TestParseCodex/other line types` (Task 1); the real-data run in Task 4 must report near-zero skipped lines.

---

### Task 1: Transcript matcher, snippet and vendor parsers

**Files:**
- Create: `internal/session/transcript.go`, `internal/session/claude.go`, `internal/session/codex.go`
- Test: `internal/session/transcript_test.go`, `internal/session/parse_test.go`

**Interfaces:**
- Produces:
  - `type transcript struct { sessionID, cwd string; terms []string; found []bool; snippetText string }`
  - `func newTranscript(terms []string) *transcript` (lower-cases terms)
  - `func (t *transcript) identify(id, cwd string)`, `func (t *transcript) add(text string)`, `func (t *transcript) matched() bool`
  - `func snippet(text, term string) string`; `const snippetSize = 160`
  - `func injected(text string, prefixes []string) bool`
  - `type parser func(line []byte) (id, cwd string, texts []string, ok bool)` — the session id and cwd a line names (or ""), its searched texts, and ok=false for an unusable line
  - `func parseClaude(line []byte) (id, cwd string, texts []string, ok bool)`, `func parseCodex(...)` with the same signature

- [ ] **Step 1: Write the matcher and snippet tests**

Create `internal/session/transcript_test.go`:

```go
package session

import (
	"strings"
	"testing"
)

func TestTranscript(t *testing.T) {
	tr := newTranscript([]string{"Process", "timeout"})
	tr.add("we looked at the process group")
	if tr.matched() {
		t.Fatal("matched with one term missing")
	}
	tr.add("then the TIMEOUT fired")
	tr.add("process again, later")
	if !tr.matched() {
		t.Fatal("terms in different messages must match")
	}
	if tr.snippetText != "process again, later" {
		t.Errorf("snippet source = %q, want the newest text with the first term", tr.snippetText)
	}
	tr.identify("s1", "/a")
	tr.identify("s2", "/b")
	if tr.sessionID != "s1" || tr.cwd != "/a" {
		t.Errorf("identify kept %q %q, want the first ones", tr.sessionID, tr.cwd)
	}
}

func TestSnippet(t *testing.T) {
	long := strings.Repeat("a ", 100) + "NEEDLE" + strings.Repeat(" b", 100)
	tests := []struct {
		name, text, term, want string
	}{
		{"short", "find the needle\nhere", "needle", "find the needle here"},
		{"case", "Find The NEEDLE", "needle", "Find The NEEDLE"},
		{"cut both ends", long, "needle", "…" + strings.Repeat("a ", 26) + "NEEDLE" + strings.Repeat(" b", 50) + "…"},
		{"rune boundary", strings.Repeat("é", 100) + "x", "x", "…" + strings.Repeat("é", 27) + "x"},
		{"no match keeps the start", "abc", "zzz", "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := snippet(tt.text, tt.term)
			if got != tt.want {
				t.Errorf("snippet = %q\nwant      %q", got, tt.want)
			}
			if body := strings.Trim(got, "…"); len(body) > snippetSize {
				t.Errorf("snippet body is %d bytes", len(body))
			}
		})
	}
}

func TestInjected(t *testing.T) {
	prefixes := []string{"<system-reminder>", "<command-"}
	for text, want := range map[string]bool{
		"  <system-reminder>x":     true,
		"<command-name>/clear":     true,
		"please fix <command-x>":   false,
		"<pasted_content>my text": false,
	} {
		if got := injected(text, prefixes); got != want {
			t.Errorf("injected(%q) = %v, want %v", text, got, want)
		}
	}
}
```

The `cut both ends` case: the needle starts at byte 200; the window starts `snippetSize/3 = 53` bytes earlier at byte 147 (a space), runs 160 bytes, and whitespace collapse turns its 53 leading bytes into 26 `"a "` pairs and its tail into 50 `" b"` pairs. The `rune boundary` case: `x` is at byte 200; byte 147 is inside an `é`, so the window starts at 146: 27 runes of `é`, then `x`, and the text ends there.

- [ ] **Step 2: Write the parser tests**

Create `internal/session/parse_test.go`:

```go
package session

import (
	"slices"
	"testing"
)

type parseCase struct {
	name    string
	lines   []string
	ok      []bool // per line
	id, cwd string
	texts   []string // searched texts, in order
}

// run feeds c.lines to parse and checks what it reported: the first id and
// cwd named, the searched texts, and which lines were usable.
func run(t *testing.T, parse parser, c parseCase) {
	t.Helper()
	var id, cwd string
	var texts []string
	for i, l := range c.lines {
		lid, lcwd, lt, ok := parse([]byte(l))
		if ok != c.ok[i] {
			t.Errorf("%s: line %d ok = %v, want %v", c.name, i, ok, c.ok[i])
		}
		if id == "" {
			id = lid
		}
		if cwd == "" {
			cwd = lcwd
		}
		texts = append(texts, lt...)
	}
	if id != c.id || cwd != c.cwd {
		t.Errorf("%s: id=%q cwd=%q, want %q %q", c.name, id, cwd, c.id, c.cwd)
	}
	if !slices.Equal(texts, c.texts) {
		t.Errorf("%s: texts = %q, want %q", c.name, texts, c.texts)
	}
}

func TestParseClaude(t *testing.T) {
	const head = `"sessionId":"s1","cwd":"/w"`
	for _, c := range []parseCase{
		{"string content", []string{`{"type":"user",` + head + `,"message":{"role":"user","content":"hello there"}}`},
			[]bool{true}, "s1", "/w", []string{"hello there"}},
		{"text blocks only", []string{`{"type":"assistant",` + head + `,"message":{"content":[{"type":"thinking","thinking":"secret plan"},{"type":"text","text":"answer"},{"type":"tool_use","name":"Bash","input":{"command":"cat .env"}}]}}`},
			[]bool{true}, "s1", "/w", []string{"answer"}},
		{"tool results not searched", []string{`{"type":"user",` + head + `,"message":{"content":[{"type":"tool_result","content":"API_KEY=x"}]}}`},
			[]bool{true}, "s1", "/w", nil},
		{"sidechain and meta skipped", []string{
			`{"type":"user",` + head + `,"isSidechain":true,"message":{"content":"sub"}}`,
			`{"type":"user",` + head + `,"isMeta":true,"message":{"content":"meta"}}`},
			[]bool{true, true}, "s1", "/w", nil},
		{"injected user text skipped", []string{
			`{"type":"user",` + head + `,"message":{"content":"<system-reminder>x</system-reminder>"}}`,
			`{"type":"user",` + head + `,"message":{"content":[{"type":"text","text":"<command-name>/clear</command-name>"}]}}`,
			`{"type":"assistant",` + head + `,"message":{"content":[{"type":"text","text":"<command-name> is fine from the assistant"}]}}`},
			[]bool{true, true, true}, "s1", "/w", []string{"<command-name> is fine from the assistant"}},
		{"other event types", []string{
			`{"type":"summary","summary":"x","leafUuid":"u"}`,
			`{"type":"file-history-snapshot","snapshot":{}}`,
			`{"type":"system",` + head + `,"content":"x"}`},
			[]bool{true, true, true}, "s1", "/w", nil},
		{"identity from the first event that has it", []string{
			`{"type":"summary"}`,
			`{"type":"user","sessionId":"s1","cwd":"/w","message":{"content":"a"}}`,
			`{"type":"user","sessionId":"s2","cwd":"/x","message":{"content":"b"}}`},
			[]bool{true, true, true}, "s1", "/w", []string{"a", "b"}},
		{"unusable lines", []string{`not json`, `{"type":"user",` + head + `,"message":{"content":42}}`},
			[]bool{false, false}, "s1", "/w", nil},
	} {
		run(t, parseClaude, c)
	}
}

func TestParseCodex(t *testing.T) {
	const meta = `{"type":"session_meta","payload":{"id":"c1","cwd":"/w","cli_version":"0.159.2"}}`
	msg := func(role, kind, text string) string {
		return `{"type":"response_item","payload":{"type":"message","role":"` + role + `","content":[{"type":"` + kind + `","text":"` + text + `"}]}}`
	}
	for _, c := range []parseCase{
		{"messages", []string{meta, msg("user", "input_text", "question"), msg("assistant", "output_text", "answer")},
			[]bool{true, true, true}, "c1", "/w", []string{"question", "answer"}},
		{"developer and injected context skipped", []string{meta,
			msg("developer", "input_text", "<permissions instructions>"),
			msg("user", "input_text", "<environment_context><cwd>/w</cwd></environment_context>")},
			[]bool{true, true, true}, "c1", "/w", nil},
		{"other line types", []string{meta,
			`{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"cat .env\"}"}}`,
			`{"type":"response_item","payload":{"type":"function_call_output","output":"API_KEY=x"}}`,
			`{"type":"response_item","payload":{"type":"reasoning","summary":[]}}`,
			`{"type":"event_msg","payload":{"type":"agent_message","message":"dup"}}`,
			`{"type":"turn_context","payload":{"cwd":"/w"}}`},
			[]bool{true, true, true, true, true, true}, "c1", "/w", nil},
		{"unusable lines", []string{`not json`, `{"type":"session_meta","payload":{"id":7}}`,
			`{"type":"response_item","payload":{"type":"message","role":"user","content":"not a list"}}`},
			[]bool{false, false, false}, "", "", nil},
	} {
		run(t, parseCodex, c)
	}
}
```

- [ ] **Step 3: Run the tests to see them fail**

Run: `go test -count=1 ./internal/session 2>&1 | tail -3`
Expected: build failure (`undefined: newTranscript`, `parseClaude`, …).

- [ ] **Step 4: Implement the matcher and snippet**

Create `internal/session/transcript.go`:

```go
// Package session is the "session" command group: it searches the coding
// agents' own transcript files where they are and writes nothing.
package session

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// snippetSize bounds the text of a snippet, before the "…" marks.
const snippetSize = 160

// transcript is what search keeps of one file while reading it: its session
// id and working directory, which terms its text contains, and the newest
// text that contains the first term.
type transcript struct {
	sessionID, cwd string
	terms          []string // lower-cased
	found          []bool
	snippetText    string
}

// parser reads one transcript line: the session id and working directory it
// names (or ""), the searched texts in it, and ok=false when the line is not
// usable. Lines of shapes it does not search are usable and carry no text.
type parser func(line []byte) (id, cwd string, texts []string, ok bool)

func newTranscript(terms []string) *transcript {
	t := &transcript{found: make([]bool, len(terms))}
	for _, term := range terms {
		t.terms = append(t.terms, strings.ToLower(term))
	}
	return t
}

// identify records the session id and working directory unless already known.
func (t *transcript) identify(id, cwd string) {
	if t.sessionID == "" {
		t.sessionID = id
	}
	if t.cwd == "" {
		t.cwd = cwd
	}
}

// add records one piece of searched text.
func (t *transcript) add(text string) {
	low := strings.ToLower(text)
	for i, term := range t.terms {
		if strings.Contains(low, term) {
			t.found[i] = true
		}
	}
	if strings.Contains(low, t.terms[0]) {
		t.snippetText = text
	}
}

// matched reports whether every term occurred in some added text.
func (t *transcript) matched() bool { return !slices.Contains(t.found, false) }

// snippet returns up to snippetSize bytes of text around the first
// case-insensitive occurrence of term, cut on rune boundaries, whitespace
// collapsed, with "…" where text was cut.
func snippet(text, term string) string {
	at := 0
	low := strings.ToLower(text)
	if i := strings.Index(low, strings.ToLower(term)); i >= 0 {
		// ToLower maps rune to rune, so the rune index carries over.
		at = byteOffset(text, utf8.RuneCountInString(low[:i]))
	}
	start := max(0, at-snippetSize/3)
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	end := min(len(text), start+snippetSize)
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	s := strings.Join(strings.Fields(text[start:end]), " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(text) {
		s += "…"
	}
	return s
}

// byteOffset returns the byte offset of rune n of s.
func byteOffset(s string, n int) int {
	for i := range s {
		if n == 0 {
			return i
		}
		n--
	}
	return len(s)
}

// injected reports whether text, ignoring leading whitespace, starts with one
// of prefixes: text the agent CLI adds to a user turn by itself.
func injected(text string, prefixes []string) bool {
	text = strings.TrimLeft(text, " \t\r\n")
	return slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(text, p) })
}
```

- [ ] **Step 5: Implement the parsers**

Create `internal/session/claude.go`:

```go
package session

import "encoding/json"

// claudeInjected starts user text that Claude Code adds by itself.
var claudeInjected = []string{"<task-notification>", "<local-command-", "<command-", "<system-reminder>"}

// parseClaude reads one line of a Claude Code transcript. Every event may
// carry sessionId and cwd; user and assistant events carry the message text.
// Events of other types are ignored, whatever their shape.
func parseClaude(line []byte) (id, cwd string, texts []string, ok bool) {
	var h struct {
		Type      string          `json:"type"`
		SessionID json.RawMessage `json:"sessionId"`
		Cwd       json.RawMessage `json:"cwd"`
	}
	if json.Unmarshal(line, &h) != nil {
		return "", "", nil, false
	}
	id, cwd = rawString(h.SessionID), rawString(h.Cwd)
	if h.Type != "user" && h.Type != "assistant" {
		return id, cwd, nil, true
	}
	var e struct {
		IsSidechain bool `json:"isSidechain"`
		IsMeta      bool `json:"isMeta"`
		Message     struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &e) != nil {
		return id, cwd, nil, false
	}
	if e.IsSidechain || e.IsMeta {
		return id, cwd, nil, true
	}
	all, ok := claudeTexts(e.Message.Content)
	if !ok {
		return id, cwd, nil, false
	}
	for _, s := range all {
		if h.Type == "user" && injected(s, claudeInjected) {
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
```

Create `internal/session/codex.go`:

```go
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
```

- [ ] **Step 6: Run the tests to see them pass**

Run: `gofmt -l . ; go vet ./internal/session && go test -count=1 ./internal/session 2>&1 | tail -5`
Expected: `ok`. If a `TestSnippet` literal disagrees with the algorithm by arithmetic only (see the note in Step 1), correct the literal and ledger it.

- [ ] **Step 7: Commit**

```bash
REF='#17' commit.sh "feat(session): transcript matcher, snippet and vendor parsers" internal/session
```

---

### Task 2: Prefilter, file discovery and Search

**Files:**
- Create: `internal/session/session.go`, `internal/session/prefilter.go`, `internal/session/search.go`
- Test: `internal/session/prefilter_test.go`, `internal/session/search_test.go`

**Interfaces:**
- Consumes: `transcript`, `newTranscript`, `snippet`, `parser`, `parseClaude`, `parseCodex` (Task 1).
- Produces:
  - `type Query struct { Terms []string; Agent, Cwd string; Since time.Duration; Limit int; Environ []string }`
  - `type Result struct { Agent, SessionID, Cwd string; UpdatedAt time.Time; Snippet string }` (JSON `agent`, `session_id`, `cwd`, `updated_at`, `snippet`)
  - `type Skipped struct { Files, Lines int }` (JSON `files`, `lines`); `type Output struct { Results []Result; Skipped Skipped }` (JSON `results`, `skipped`)
  - `type InvalidQueryError struct{ Reason string }`
  - `func Agents() []string`; `const DefaultLimit = 20`
  - `func Search(ctx context.Context, q Query) (Output, error)` — error is `*InvalidQueryError`, or `ctx.Err()`.

- [ ] **Step 1: Write the prefilter tests**

Create `internal/session/prefilter_test.go`:

```go
package session

import (
	"bytes"
	"strings"
	"testing"
)

func TestNeedles(t *testing.T) {
	got := needles([]string{"Process", `a"b`, `c\d`, "ok"})
	if len(got) != 2 || string(got[0]) != "process" || string(got[1]) != "ok" {
		t.Errorf("needles = %q", got)
	}
}

func TestMayMatch(t *testing.T) {
	pad := strings.Repeat("x", chunkSize-3)
	tests := []struct {
		name  string
		text  string
		terms []string
		want  bool
	}{
		{"all present", "the Process group timed out", []string{"process", "timed"}, true},
		{"one missing", "the process group", []string{"process", "timeout"}, false},
		{"split across chunks", pad + "NEEDLE" + pad, []string{"needle"}, true},
		{"terms in different chunks", "alpha" + pad + pad + "omega", []string{"alpha", "omega"}, true},
		{"no needles", "anything", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ns [][]byte
			for _, term := range tt.terms {
				ns = append(ns, []byte(term))
			}
			got, err := mayMatch(bytes.NewReader([]byte(tt.text)), ns)
			if err != nil || got != tt.want {
				t.Errorf("mayMatch = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Write the search tests**

Create `internal/session/search_test.go`:

```go
package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// home builds a HOME with transcripts and returns its environment.
type home struct {
	t   *testing.T
	dir string
}

func newHome(t *testing.T) home { return home{t, t.TempDir()} }

func (h home) env() []string { return []string{"HOME=" + h.dir} }

func (h home) write(rel string, mtime time.Time, lines ...string) string {
	h.t.Helper()
	p := filepath.Join(h.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		h.t.Fatal(err)
	}
	return p
}

func claudeLine(id, cwd, role, text string) string {
	return `{"type":"` + role + `","sessionId":"` + id + `","cwd":"` + cwd + `","message":{"role":"` + role + `","content":"` + text + `"}}`
}

func codexLines(id, cwd, text string) []string {
	return []string{
		`{"type":"session_meta","payload":{"id":"` + id + `","cwd":"` + cwd + `"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + text + `"}]}}`,
	}
}

func ids(out Output) []string {
	var s []string
	for _, r := range out.Results {
		s = append(s, r.Agent+":"+r.SessionID)
	}
	return s
}

func search(t *testing.T, q Query) Output {
	t.Helper()
	if q.Limit == 0 {
		q.Limit = DefaultLimit
	}
	out, err := Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSearch(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	h := newHome(t)
	h.write(".claude/projects/-w/c1.jsonl", now.Add(-1*time.Hour),
		claudeLine("c1", "/w/aits", "user", "the process group"),
		claudeLine("c1", "/w/aits", "assistant", "fixed the timeout"))
	h.write(".claude/projects/-w/c2.jsonl", now.Add(-48*time.Hour), claudeLine("c2", "/w/other", "user", "process only"))
	h.write(".claude/projects/-w/c1/subagents/agent-x.jsonl", now, claudeLine("c1", "/w/aits", "user", "process timeout in a subagent"))
	h.write(".codex/sessions/2026/10/14/rollout-2026-10-14T00-00-00-x1.jsonl", now.Add(-2*time.Hour), codexLines("x1", "/w/aits/sub", "Process and TIMEOUT")...)
	h.write(".claude/projects/-w/q.jsonl", now.Add(-3*time.Hour), claudeLine("q", "/w", "user", `say \"hi\" now`))

	tests := []struct {
		name string
		q    Query
		want []string
	}{
		{"both agents, newest first, subagent not searched", Query{Terms: []string{"process", "timeout"}}, []string{"claude:c1", "codex:x1"}},
		{"agent filter", Query{Terms: []string{"process"}, Agent: "codex"}, []string{"codex:x1"}},
		{"cwd filter is a path prefix", Query{Terms: []string{"process"}, Cwd: "/w/aits"}, []string{"claude:c1", "codex:x1"}},
		{"cwd boundary", Query{Terms: []string{"process"}, Cwd: "/w/ait"}, nil},
		{"since", Query{Terms: []string{"process"}, Since: 24 * time.Hour}, []string{"claude:c1", "codex:x1"}},
		{"limit", Query{Terms: []string{"process"}, Limit: 1}, []string{"claude:c1"}},
		{"quote in term", Query{Terms: []string{`"hi"`}}, []string{"claude:q"}},
		{"no match", Query{Terms: []string{"nothing-like-this"}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := tt.q
			q.Environ = h.env()
			out := search(t, q)
			if got := ids(out); !slices.Equal(got, tt.want) {
				t.Errorf("results = %q, want %q", got, tt.want)
			}
			if out.Results == nil {
				t.Errorf("Results is nil; want an empty slice")
			}
		})
	}
	out := search(t, Query{Terms: []string{"timeout"}, Agent: "claude", Environ: h.env()})
	if r := out.Results[0]; r.Cwd != "/w/aits" || !r.UpdatedAt.Equal(now.Add(-time.Hour).UTC()) || r.Snippet != "fixed the timeout" {
		t.Errorf("result = %+v", r)
	}
}

func TestSearchSkipped(t *testing.T) {
	h := newHome(t)
	now := time.Now()
	h.write(".claude/projects/-w/good.jsonl", now, "not json", claudeLine("good", "/w", "user", "needle"))
	h.write(".claude/projects/-w/noid.jsonl", now, `{"type":"user","message":{"content":"needle"}}`)
	out := search(t, Query{Terms: []string{"needle"}, Environ: h.env()})
	if got := ids(out); !slices.Equal(got, []string{"claude:good"}) || out.Skipped != (Skipped{Files: 1, Lines: 1}) {
		t.Errorf("results %q, skipped %+v", got, out.Skipped)
	}
}

func TestSearchVendorRoots(t *testing.T) {
	h := newHome(t)
	now := time.Now()
	h.write("cc/projects/-w/a.jsonl", now, claudeLine("a", "/w", "user", "needle"))
	h.write("cx/sessions/2026/01/01/rollout-x-b.jsonl", now, codexLines("b", "/w", "needle")...)
	env := append(h.env(), "CLAUDE_CONFIG_DIR="+filepath.Join(h.dir, "cc"), "CODEX_HOME="+filepath.Join(h.dir, "cx"))
	if got := ids(search(t, Query{Terms: []string{"needle"}, Environ: env})); !slices.Equal(got, []string{"claude:a", "codex:b"}) {
		t.Errorf("results = %q", got)
	}
	if got := search(t, Query{Terms: []string{"needle"}, Environ: []string{"HOME=" + t.TempDir()}}); len(got.Results) != 0 || got.Skipped != (Skipped{}) {
		t.Errorf("missing vendor dirs: %+v", got)
	}
}

func TestFilesEscapesRoot(t *testing.T) {
	h := home{t, filepath.Join(t.TempDir(), "we[ird]*")}
	h.write(".claude/projects/-w/a.jsonl", time.Now(), claudeLine("a", "/w", "user", "needle"))
	if got := ids(search(t, Query{Terms: []string{"needle"}, Environ: h.env()})); !slices.Equal(got, []string{"claude:a"}) {
		t.Errorf("results = %q", got)
	}
}

func TestSearchCancelled(t *testing.T) {
	h := newHome(t)
	for i := range 200 {
		h.write(".claude/projects/-w/"+strings.Repeat("a", i+1)+".jsonl", time.Now(), claudeLine("s", "/w", "user", "needle"))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Search(ctx, Query{Terms: []string{"needle"}, Limit: 1, Environ: h.env()}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestQueryValidate(t *testing.T) {
	good := Query{Terms: []string{"x"}, Limit: 1}
	for name, edit := range map[string]func(*Query){
		"no terms":      func(q *Query) { q.Terms = nil },
		"empty term":    func(q *Query) { q.Terms = []string{""} },
		"unknown agent": func(q *Query) { q.Agent = "nope" },
		"relative cwd":  func(q *Query) { q.Cwd = "rel" },
		"negative since": func(q *Query) { q.Since = -time.Second },
		"zero limit":    func(q *Query) { q.Limit = 0 },
	} {
		q := good
		edit(&q)
		var invalid *InvalidQueryError
		if _, err := Search(context.Background(), q); !errors.As(err, &invalid) {
			t.Errorf("%s: err = %v, want *InvalidQueryError", name, err)
		}
	}
}

func TestAgents(t *testing.T) {
	if got := Agents(); !slices.Equal(got, []string{"claude", "codex"}) {
		t.Errorf("Agents() = %q", got)
	}
}
```

- [ ] **Step 3: Run the tests to see them fail**

Run: `go test -count=1 ./internal/session 2>&1 | tail -3`
Expected: build failure (`undefined: needles`, `Search`, `Query`, …).

- [ ] **Step 4: Implement the query types**

Create `internal/session/session.go`:

```go
package session

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// DefaultLimit is the number of results the command asks for by default.
const DefaultLimit = 20

// Query is one search.
type Query struct {
	Terms   []string      // all must occur in the session's text, ignoring case
	Agent   string        // one of Agents(), or "" for all
	Cwd     string        // absolute; only sessions in or below it; "" for any
	Since   time.Duration // only files modified within it; 0 for any
	Limit   int           // at most this many results; at least 1
	Environ []string      // KEY=VALUE, for HOME, CLAUDE_CONFIG_DIR and CODEX_HOME
}

// Result is one matching session.
type Result struct {
	Agent     string    `json:"agent"`
	SessionID string    `json:"session_id"`
	Cwd       string    `json:"cwd"`
	UpdatedAt time.Time `json:"updated_at"`
	Snippet   string    `json:"snippet"`
}

// Skipped counts what could not be read, so a partial answer is never silent
// (P6).
type Skipped struct {
	Files int `json:"files"`
	Lines int `json:"lines"`
}

// Output is the answer to a Query.
type Output struct {
	Results []Result `json:"results"`
	Skipped Skipped  `json:"skipped"`
}

// InvalidQueryError means the query was rejected before anything was read.
type InvalidQueryError struct{ Reason string }

func (e *InvalidQueryError) Error() string { return "invalid query: " + e.Reason }

func invalid(format string, a ...any) error {
	return &InvalidQueryError{Reason: fmt.Sprintf(format, a...)}
}

// Agents returns the agents whose transcripts are searched, sorted.
func Agents() []string { return slices.Sorted(maps.Keys(vendors)) }

func (q Query) validate() error {
	if len(q.Terms) == 0 {
		return invalid("no search terms")
	}
	if slices.Contains(q.Terms, "") {
		return invalid("empty search term")
	}
	if _, ok := vendors[q.Agent]; q.Agent != "" && !ok {
		return invalid("unknown agent %q (one of: %s)", q.Agent, strings.Join(Agents(), ", "))
	}
	if q.Cwd != "" && !filepath.IsAbs(q.Cwd) {
		return invalid("directory %q is not absolute", q.Cwd)
	}
	if q.Since < 0 {
		return invalid("since must be positive, got %s", q.Since)
	}
	if q.Limit < 1 {
		return invalid("limit must be at least 1, got %d", q.Limit)
	}
	return nil
}
```

- [ ] **Step 5: Implement the prefilter**

Create `internal/session/prefilter.go`:

```go
package session

import (
	"bytes"
	"io"
	"strings"
)

// chunkSize is how much of a file the prefilter reads at a time.
const chunkSize = 1 << 20

// needles returns the lower-cased terms the byte prefilter looks for. A term
// with a byte JSON escapes (quote, backslash, control character) would not
// match the raw file, so it is left to the parse step.
func needles(terms []string) [][]byte {
	var out [][]byte
	for _, t := range terms {
		if strings.ContainsFunc(t, func(r rune) bool { return r == '"' || r == '\\' || r < 0x20 }) {
			continue
		}
		out = append(out, []byte(strings.ToLower(t)))
	}
	return out
}

// mayMatch reports whether every needle occurs in r, ignoring case. It reads r
// chunk by chunk, keeping an overlap so a needle split between two chunks is
// still seen; memory does not grow with the file (P8).
func mayMatch(r io.Reader, needles [][]byte) (bool, error) {
	if len(needles) == 0 {
		return true, nil
	}
	longest := 0
	for _, n := range needles {
		longest = max(longest, len(n))
	}
	// Lower-casing can shrink a rune (up to 3 bytes to 1), so keep enough of
	// the previous chunk to hold the longest needle's original spelling.
	overlap := 4 * longest
	found := make([]bool, len(needles))
	left := len(needles)
	chunk := make([]byte, chunkSize)
	window := make([]byte, 0, overlap+chunkSize)
	for {
		n, err := io.ReadFull(r, chunk)
		window = append(window, chunk[:n]...)
		low := bytes.ToLower(window)
		for i, nd := range needles {
			if !found[i] && bytes.Contains(low, nd) {
				found[i] = true
				left--
			}
		}
		switch {
		case left == 0:
			return true, nil
		case err == io.EOF || err == io.ErrUnexpectedEOF:
			return false, nil
		case err != nil:
			return false, err
		}
		if len(window) > overlap {
			window = append(window[:0], window[len(window)-overlap:]...)
		}
	}
}
```

- [ ] **Step 6: Implement discovery and Search**

Create `internal/session/search.go`:

```go
package session

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// maxLine bounds one transcript line; longer lines are skipped and counted.
const maxLine = 64 << 20

// vendor describes where one agent keeps its transcripts and how to read
// them.
type vendor struct {
	rootVar  string // environment variable naming the root
	rootHome string // the root under HOME otherwise
	pattern  string // transcript files under the root, for filepath.Glob
	parse    parser
}

var vendors = map[string]vendor{
	"claude": {rootVar: "CLAUDE_CONFIG_DIR", rootHome: ".claude", pattern: "projects/*/*.jsonl", parse: parseClaude},
	"codex":  {rootVar: "CODEX_HOME", rootHome: ".codex", pattern: "sessions/*/*/*/rollout-*.jsonl", parse: parseCodex},
}

// files lists the vendor's transcript files. A missing root lists nothing.
func (v vendor) files(environ []string) []string {
	root := lookup(environ, v.rootVar)
	if root == "" {
		home := lookup(environ, "HOME")
		if home == "" {
			return nil
		}
		root = filepath.Join(home, v.rootHome)
	}
	// The pattern is fixed, so Glob cannot fail; it skips unreadable
	// directories without saying so (a documented limit).
	paths, _ := filepath.Glob(filepath.Join(escapeGlob(root), v.pattern))
	return paths
}

// lookup returns the last value of name in environ, or "".
func lookup(environ []string, name string) string {
	val := ""
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			val = v
		}
	}
	return val
}

// escapeGlob quotes the pattern characters of a literal path.
func escapeGlob(p string) string {
	var b strings.Builder
	for _, r := range p {
		if strings.ContainsRune(`*?[\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

type job struct {
	agent string
	path  string
	mtime time.Time
	parse parser
}

type outcome struct {
	result  *Result
	lines   int  // unusable lines
	badFile bool // unreadable, or no session id or cwd
}

// Search runs q over the selected agents' transcripts, in parallel. It
// returns ctx's error if ctx is cancelled before every file is examined.
func Search(ctx context.Context, q Query) (Output, error) {
	if err := q.validate(); err != nil {
		return Output{}, err
	}
	out := Output{Results: []Result{}}
	var cutoff time.Time
	if q.Since > 0 {
		cutoff = time.Now().Add(-q.Since)
	}
	var jobs []job
	for _, name := range Agents() {
		if q.Agent != "" && q.Agent != name {
			continue
		}
		v := vendors[name]
		for _, p := range v.files(q.Environ) {
			fi, err := os.Stat(p)
			if err != nil {
				out.Skipped.Files++
				continue
			}
			if fi.ModTime().Before(cutoff) {
				continue
			}
			jobs = append(jobs, job{agent: name, path: p, mtime: fi.ModTime(), parse: v.parse})
		}
	}

	ns := needles(q.Terms)
	outcomes := make([]outcome, len(jobs))
	next := make(chan int)
	var wg sync.WaitGroup
	for range runtime.NumCPU() {
		wg.Go(func() {
			for i := range next {
				outcomes[i] = examine(jobs[i], q, ns)
			}
		})
	}
feed:
	for i := range jobs {
		select {
		case next <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return Output{}, err
	}

	for _, o := range outcomes {
		out.Skipped.Lines += o.lines
		if o.badFile {
			out.Skipped.Files++
		}
		if o.result != nil {
			out.Results = append(out.Results, *o.result)
		}
	}
	slices.SortFunc(out.Results, func(a, b Result) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.SessionID, b.SessionID)
	})
	if len(out.Results) > q.Limit {
		out.Results = out.Results[:q.Limit]
	}
	return out, nil
}

// examine prefilters one file and, if every term may occur, parses it.
func examine(j job, q Query, ns [][]byte) outcome {
	f, err := os.Open(j.path)
	if err != nil {
		return outcome{badFile: true}
	}
	defer f.Close()
	if ok, err := mayMatch(f, ns); err != nil {
		return outcome{badFile: true}
	} else if !ok {
		return outcome{}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return outcome{badFile: true}
	}
	t := newTranscript(q.Terms)
	bad, err := eachLine(f, func(line []byte) bool {
		id, cwd, texts, ok := j.parse(line)
		t.identify(id, cwd)
		for _, s := range texts {
			t.add(s)
		}
		return ok
	})
	if err != nil || t.sessionID == "" || t.cwd == "" {
		return outcome{lines: bad, badFile: true}
	}
	if !t.matched() || !within(t.cwd, q.Cwd) {
		return outcome{lines: bad}
	}
	return outcome{lines: bad, result: &Result{
		Agent:     j.agent,
		SessionID: t.sessionID,
		Cwd:       t.cwd,
		UpdatedAt: j.mtime.UTC().Truncate(time.Second),
		Snippet:   snippet(t.snippetText, t.terms[0]),
	}}
}

// eachLine calls fn for every non-blank line of r and returns how many lines
// were unusable: fn returned false, or the line exceeded maxLine.
func eachLine(r io.Reader, fn func([]byte) bool) (int, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	bad := 0
	var line []byte
	tooLong := false
	for {
		part, err := br.ReadSlice('\n')
		if tooLong || len(line)+len(part) > maxLine {
			tooLong = true
		} else {
			line = append(line, part...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if l := bytes.TrimSpace(line); tooLong || (len(l) > 0 && !fn(l)) {
			bad++
		}
		line, tooLong = line[:0], false
		if err == io.EOF {
			return bad, nil
		}
		if err != nil {
			return bad, err
		}
	}
}

// within reports whether dir is base or below it; an empty base matches all.
func within(dir, base string) bool {
	if base == "" {
		return true
	}
	dir, base = filepath.Clean(dir), filepath.Clean(base)
	return dir == base || strings.HasPrefix(dir, strings.TrimSuffix(base, "/")+"/")
}
```

- [ ] **Step 7: Run the tests to see them pass**

Run: `gofmt -l . ; go vet ./internal/session && go test -count=1 -race ./internal/session 2>&1 | tail -5`
Expected: `ok`.

- [ ] **Step 8: Commit**

```bash
REF='#17' commit.sh "feat(session): search transcripts in parallel with a chunked prefilter" internal/session
```

---

### Task 3: CLI group, docs and functional tests

**Files:**
- Create: `internal/cli/session.go`, `cmd/aits/session_test.go`
- Modify: `internal/cli/command.go` (`Root`: add `sessionGroup()`), `README.md`, `docs/cli.md` (regenerated)

**Interfaces:**
- Consumes: `session.Search`, `session.Query`, `session.Output`, `session.InvalidQueryError`, `session.Agents`, `session.DefaultLimit` (Task 2); `shell.Agents()`; test helpers `call`, `vendor`, `readRecord`, `decode` from `cmd/aits/e2e_test.go`.
- Produces: `aits session search`.

- [ ] **Step 1: Write the functional tests**

Create `cmd/aits/session_test.go`:

```go
package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-in-the-shell/aits/internal/session"
	"github.com/agent-in-the-shell/aits/internal/shell"
)

// writeTranscript writes lines to rel under home with the given mtime.
func writeTranscript(t *testing.T, home, rel string, mtime time.Time, lines ...string) {
	t.Helper()
	p := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// transcripts builds a HOME with one Claude and one Codex conversation about
// the same thing, and noise that must not match.
func transcripts(t *testing.T) string {
	home := t.TempDir()
	now := time.Now()
	writeTranscript(t, home, ".claude/projects/-w/sess-claude.jsonl", now.Add(-time.Hour),
		`{"type":"user","sessionId":"sess-claude","cwd":"/w/aits","message":{"role":"user","content":"why does the process group leak?"}}`,
		`{"type":"assistant","sessionId":"sess-claude","cwd":"/w/aits","message":{"content":[{"type":"thinking","thinking":"secret-thought"},{"type":"text","text":"the timeout never fired"},{"type":"tool_use","name":"Bash","input":{"command":"echo tool-input-word"}}]}}`,
		`{"type":"user","sessionId":"sess-claude","cwd":"/w/aits","message":{"content":[{"type":"tool_result","content":"tool-output-word"}]}}`,
		`not json`)
	writeTranscript(t, home, ".codex/sessions/2026/10/14/rollout-2026-10-14T00-00-00-sess-codex.jsonl", now.Add(-2*time.Hour),
		`{"type":"session_meta","payload":{"id":"sess-codex","cwd":"/w/aits"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>injected-word</environment_context>"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Process group TIMEOUT fixed"}]}}`)
	return home
}

type searchOutput struct {
	Results []struct {
		Agent     string `json:"agent"`
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
		UpdatedAt string `json:"updated_at"`
		Snippet   string `json:"snippet"`
	} `json:"results"`
	Skipped struct {
		Files int `json:"files"`
		Lines int `json:"lines"`
	} `json:"skipped"`
}

// decodeSearch parses stdout as exactly one search object (P5).
func decodeSearch(t *testing.T, stdout string) searchOutput {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	dec.DisallowUnknownFields()
	var o searchOutput
	if err := dec.Decode(&o); err != nil {
		t.Fatalf("stdout is not one search object: %v\n%s", err, stdout)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("stdout holds more than one JSON value: %q", stdout)
	}
	return o
}

func found(o searchOutput) []string {
	var s []string
	for _, r := range o.Results {
		s = append(s, r.Agent+":"+r.SessionID)
	}
	return s
}

func TestSessionSearch(t *testing.T) {
	home := transcripts(t)
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"both agents, newest first", []string{"process", "TIMEOUT"}, []string{"claude:sess-claude", "codex:sess-codex"}},
		{"terms in one argument", []string{"process timeout"}, []string{"claude:sess-claude", "codex:sess-codex"}},
		{"agent", []string{"--agent", "codex", "process"}, []string{"codex:sess-codex"}},
		{"cwd", []string{"--cwd", "/w/other", "process"}, nil},
		{"since", []string{"--since", "90m", "process"}, []string{"claude:sess-claude"}},
		{"limit", []string{"--limit", "1", "process"}, []string{"claude:sess-claude"}},
		{"thinking not searched", []string{"secret-thought"}, nil},
		{"tool input not searched", []string{"tool-input-word"}, nil},
		{"tool output not searched", []string{"tool-output-word"}, nil},
		{"injected text not searched", []string{"injected-word"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := call{env: []string{"HOME=" + home}, args: append([]string{"session", "search", "--json"}, tt.args...)}.run(t)
			o := decodeSearch(t, out.stdout)
			if out.code != 0 || out.stderr != "" || !slices.Equal(found(o), tt.want) {
				t.Fatalf("code=%d stderr=%q results=%q, want %q", out.code, out.stderr, found(o), tt.want)
			}
		})
	}
}

func TestSessionSearchResultFields(t *testing.T) {
	home := transcripts(t)
	out := call{env: []string{"HOME=" + home}, args: []string{"session", "search", "--json", "--agent", "claude", "timeout"}}.run(t)
	o := decodeSearch(t, out.stdout)
	if len(o.Results) != 1 || o.Skipped.Lines != 1 || o.Skipped.Files != 0 {
		t.Fatalf("output = %+v", o)
	}
	r := o.Results[0]
	if r.Cwd != "/w/aits" || r.Snippet != "the timeout never fired" || !strings.HasSuffix(r.UpdatedAt, "Z") {
		t.Errorf("result = %+v", r)
	}
}

func TestSessionSearchText(t *testing.T) {
	home := transcripts(t)
	out := call{env: []string{"HOME=" + home}, args: []string{"session", "search", "--agent", "claude", "timeout"}}.run(t)
	lines := strings.Split(strings.TrimSuffix(out.stdout, "\n"), "\n")
	if out.code != 0 || len(lines) != 2 || !strings.Contains(lines[0], "  claude  sess-claude  /w/aits") ||
		lines[1] != "    the timeout never fired" || out.stderr != "aits: skipped 0 files and 1 lines that could not be read\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
	out = call{env: []string{"HOME=" + home}, args: []string{"session", "search", "nothing-like-this"}}.run(t)
	if out.code != 0 || out.stdout != "" || out.stderr != "" {
		t.Errorf("no match: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
}

func TestSessionSearchReadOnlyHome(t *testing.T) {
	home := transcripts(t)
	_ = filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o555)
		}
		return nil
	})
	t.Cleanup(func() {
		_ = filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	out := call{env: []string{"HOME=" + home}, args: []string{"session", "search", "--json", "process"}}.run(t)
	if o := decodeSearch(t, out.stdout); out.code != 0 || len(o.Results) != 2 {
		t.Fatalf("code=%d results=%q", out.code, found(o))
	}
}

func TestSessionSearchInvalidInput(t *testing.T) {
	for name, args := range map[string][]string{
		"no terms":      {},
		"blank term":    {"  "},
		"unknown agent": {"--agent", "nope", "x"},
		"bad since":     {"--since", "soon", "x"},
		"zero since":    {"--since", "0s", "x"},
		"zero limit":    {"--limit", "0", "x"},
	} {
		out := call{args: append([]string{"session", "search", "--json"}, args...)}.run(t)
		if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "invalid_input" {
			t.Errorf("%s: code=%d stdout=%q", name, out.code, out.stdout)
		}
	}
}

// The id search returns is the one shell submit --session continues.
func TestSessionSearchIDResumes(t *testing.T) {
	home := transcripts(t)
	out := call{env: []string{"HOME=" + home}, args: []string{"session", "search", "--json", "--agent", "codex", "process"}}.run(t)
	id := decodeSearch(t, out.stdout).Results[0].SessionID
	bin := vendorAs(t, "codex", "ok")
	if out := call{path: bin, args: agentArgs("codex", "--session", id, "--json", "go on")}.run(t); out.code != 0 {
		t.Fatalf("resume: code=%d stdout=%q", out.code, out.stdout)
	}
	if args := readRecord(t, bin).Args; !slices.Equal(args, []string{"exec", "--json", "resume", id, "-"}) {
		t.Errorf("argv = %q", args)
	}
}

// TestOneVendorVocabulary enforces P4's one vocabulary of vendor names across
// groups, without either group importing the other (P1).
func TestOneVendorVocabulary(t *testing.T) {
	if s, sh := session.Agents(), shell.Agents(); !slices.Equal(s, sh) {
		t.Errorf("session knows %q, shell knows %q", s, sh)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test -count=1 ./cmd/aits -run 'TestSession|TestOneVendor' 2>&1 | grep -E "^(--- FAIL|ok|FAIL)" | head`
Expected: the `TestSessionSearch*` tests FAIL (`unknown_command`: there is no `session` group yet); `TestOneVendorVocabulary` passes.

- [ ] **Step 3: Implement the CLI group**

Create `internal/cli/session.go`:

```go
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/agent-in-the-shell/aits/internal/session"
)

func sessionGroup() *Command {
	return &Command{
		Name:     "session",
		Summary:  "Search the coding agents' own conversation transcripts.",
		Children: []*Command{searchCommand()},
	}
}

// searchDetails documents the output of "aits session search" (P5).
const searchDetails = `Output:
  With --json, stdout is one JSON object:
  {"results":[...],"skipped":{"files":N,"lines":N}}. Without --json, each
  result is a line "UPDATED_AT  AGENT  SESSION_ID  CWD" and an indented
  snippet, and anything skipped is reported on stderr. No match is not an
  error.

Result fields:
  agent        The agent whose transcript matched.
  session_id   Pass it to "aits shell submit --session" to continue.
  cwd          The session's working directory.
  updated_at   When the transcript file last changed, in UTC; newest first.
  snippet      Text around the first term in the newest message with it.

Skipped:
  files        Transcripts that could not be read or name no session or cwd.
  lines        Lines that were not JSON or were longer than 64 MiB.

Searched:
  User and assistant message text, every term somewhere in the session,
  ignoring case. Not tool calls or their output, model thinking, subagent
  transcripts, or text the agent CLI adds to a turn by itself.

Transcripts:
  claude  $CLAUDE_CONFIG_DIR/projects/*/*.jsonl (default ~/.claude)
  codex   $CODEX_HOME/sessions/*/*/*/rollout-*.jsonl (default ~/.codex)
  Subdirectories that cannot be read are passed over without a count; if a
  vendor changes this layout, its sessions are not found.

Exit codes:
  0  The search ran, with or without results.
  1  Stopped by a signal, or an unexpected failure.
  2  Usage error or invalid input.
`

func searchCommand() *Command {
	return &Command{
		Name:    "search",
		Args:    "TERM...",
		Summary: "Find conversations that contain every term, with an id to continue them.",
		Details: searchDetails,
		Setup: func(fs *flag.FlagSet) Runner {
			agent := fs.String("agent", "", "Search only `AGENT`'s transcripts. One of: "+strings.Join(session.Agents(), ", ")+".")
			cwd := fs.String("cwd", "", "Only sessions whose working directory is `DIR` or below it.")
			var since time.Duration
			fs.Func("since", "Only transcripts changed within `DURATION`, e.g. 72h.", func(v string) error {
				d, err := time.ParseDuration(v)
				if err != nil {
					return err
				}
				if d <= 0 {
					return errors.New("must be positive")
				}
				since = d
				return nil
			})
			limit := fs.Int("limit", session.DefaultLimit, "Return at most `N` results.")
			return func(ctx context.Context, env *Env, args []string) (int, error) {
				var terms []string
				for _, a := range args {
					terms = append(terms, strings.Fields(a)...)
				}
				dir := *cwd
				if dir != "" {
					abs, err := filepath.Abs(dir)
					if err != nil {
						return 0, failure("internal", "resolve --cwd: %v", err)
					}
					dir = abs
				}
				out, err := session.Search(ctx, session.Query{
					Terms: terms, Agent: *agent, Cwd: dir, Since: since, Limit: *limit, Environ: env.Environ,
				})
				if err != nil {
					return 0, searchError(err)
				}
				writeSearch(env, out)
				return exitOK, nil
			}
		},
	}
}

func searchError(err error) error {
	var invalid *session.InvalidQueryError
	switch {
	case errors.As(err, &invalid):
		return usageError("invalid_input", "%s", invalid.Reason)
	case errors.Is(err, context.Canceled):
		return failure("stopped", "stopped by a signal")
	}
	return failure("internal", "%v", err)
}

func writeSearch(env *Env, out session.Output) {
	if env.JSON {
		writeJSON(env.Stdout, out)
		return
	}
	for _, r := range out.Results {
		fmt.Fprintf(env.Stdout, "%s  %s  %s  %s\n    %s\n", r.UpdatedAt.Format(time.RFC3339), r.Agent, r.SessionID, r.Cwd, r.Snippet)
	}
	if s := out.Skipped; s.Files+s.Lines > 0 {
		fmt.Fprintf(env.Stderr, "aits: skipped %d files and %d lines that could not be read\n", s.Files, s.Lines)
	}
}
```

In `internal/cli/command.go` `Root`, change `Children: []*Command{shellGroup()},` to `Children: []*Command{shellGroup(), sessionGroup()},`.

- [ ] **Step 4: Run the tests, regenerate the reference**

Run: `go test -count=1 ./cmd/aits -run TestCLIDocs -update && gofmt -l . ; go vet ./... && go test -count=1 ./... 2>&1 | tail -4`
Expected: all `ok` (the grammar tests now walk `session` and `session search` as well).

- [ ] **Step 5: README**

In `README.md`, under `## Usage`, after the `--session` example, add:

```

Find an earlier conversation and its id:

    aits session search --json "process group timeout"
```

and in the planned-groups table change the `aits session` row's job to `Full-text search over vendor transcripts; get back a resumable session id (available)`.

- [ ] **Step 6: Commit**

```bash
REF='#17' commit.sh "feat(cli): aits session search" internal/cli/session.go internal/cli/command.go cmd/aits/session_test.go README.md docs/cli.md
```

---

### Task 4: Measure on real transcripts

**Files:** none (results go into the pull request description).

- [ ] **Step 1: Build and run on this machine's transcripts**

Run (prints counts and timings only, no transcript text):

```bash
SCRATCH=<the session scratchpad directory>
go build -o "$SCRATCH/aits" ./cmd/aits
for q in "process group" "timeout" "zzzz-no-such-word"; do
  /usr/bin/time -p "$SCRATCH/aits" session search --json --limit 5 "$q" \
    | python3 -I -c 'import sys,json; o=json.load(sys.stdin); print(len(o["results"]), o["skipped"])'
done 2>&1
```

Expected: each query finishes in under 10 s (warm cache, second run of each) and `skipped.lines` is small relative to the files examined. A large `skipped` count means a parser is too strict for some real event shape: find the shape (keys only, never print text), add it as a parser test case, fix, and re-run.

- [ ] **Step 2: Record**

Write the timings, file counts and skipped counts into the pull request's Verification section.
