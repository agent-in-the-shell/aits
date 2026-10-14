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
	out = call{path: bin, args: agentArgs("codex", "--session", id, "--json", "go on")}.run(t)
	if out.code != 0 {
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
