package main

import (
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

func decodeSearch(t *testing.T, stdout string) searchOutput {
	t.Helper()
	return decodeOne[searchOutput](t, stdout)
}

func found(o searchOutput) []string {
	var s []string
	for _, r := range o.Results {
		s = append(s, r.Agent+":"+r.SessionID)
	}
	return s
}

// searchJSON runs aits session search --json with env and args, and checks
// that it exits 0 with nothing on stderr.
func searchJSON(t *testing.T, env []string, args ...string) searchOutput {
	t.Helper()
	out := call{env: env, args: append([]string{"session", "search", "--json"}, args...)}.run(t)
	if out.code != 0 || out.stderr != "" {
		t.Fatalf("search %q: code=%d stderr=%q", args, out.code, out.stderr)
	}
	return decodeSearch(t, out.stdout)
}

// noResults is search --json finding nothing and skipping nothing.
const noResults = `{"results":[],"skipped":{"files":0,"lines":0}}` + "\n"

// claudeUser is a Claude user event of session id in /w with text.
func claudeUser(id, text string) string {
	return `{"type":"user","sessionId":"` + id + `","cwd":"/w","message":{"role":"user","content":"` + text + `"}}`
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
			if got := found(searchJSON(t, []string{"HOME=" + home}, tt.args...)); !slices.Equal(got, tt.want) {
				t.Fatalf("results=%q, want %q", got, tt.want)
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
		"no terms":       {},
		"blank term":     {"  "},
		"unknown agent":  {"--agent", "nope", "x"},
		"bad since":      {"--since", "soon", "x"},
		"zero since":     {"--since", "0s", "x"},
		"zero limit":     {"--limit", "0", "x"},
		"unknown source": {"--source", "both", "x"},
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

// Vendors usually record the working directory with symlinks resolved, so
// --cwd also matches the resolved form: on macOS /tmp and $TMPDIR are symlinks.
func TestSessionSearchCwdThroughSymlink(t *testing.T) {
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	writeTranscript(t, home, ".claude/projects/-w/s.jsonl", time.Now(),
		`{"type":"user","sessionId":"s","cwd":"`+real+`","message":{"content":"needle"}}`)
	for _, c := range []call{
		{env: []string{"HOME=" + home}, args: []string{"session", "search", "--json", "--cwd", link, "needle"}},
		{env: []string{"HOME=" + home}, dir: link, args: []string{"session", "search", "--json", "--cwd", ".", "needle"}},
	} {
		c.env = append(c.env, "PWD="+link)
		out := c.run(t)
		if got := found(decodeSearch(t, out.stdout)); !slices.Equal(got, []string{"claude:s"}) {
			t.Errorf("aits %s (dir %q): results %q", strings.Join(c.args, " "), c.dir, got)
		}
	}
}

// Some sessions record the directory as the agent was given it, symlinks
// unresolved, so --cwd matches either form.
func TestSessionSearchCwdRecordedUnresolved(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	writeTranscript(t, home, ".claude/projects/-w/s.jsonl", time.Now(),
		`{"type":"user","sessionId":"s","cwd":"`+link+`","message":{"content":"needle"}}`)
	out := call{env: []string{"HOME=" + home}, args: []string{"session", "search", "--json", "--cwd", link, "needle"}}.run(t)
	if got := found(decodeSearch(t, out.stdout)); !slices.Equal(got, []string{"claude:s"}) {
		t.Errorf("results %q", got)
	}
}

// An explicitly empty --agent or --cwd is refused, never read as "all" (P6:
// a filter is not silently widened).
func TestSessionSearchEmptyFilterFlags(t *testing.T) {
	for flag, want := range map[string]string{
		"agent":  "needs an agent (one of: claude, codex)",
		"cwd":    "needs a directory",
		"source": "needs a source (one of: interactive, scripted)",
	} {
		for _, args := range [][]string{{"--" + flag + "=", "x"}, {"--" + flag, "", "x"}} {
			name := strings.Join(args, " ")
			out := call{args: append([]string{"session", "search", "--json"}, args...)}.run(t)
			r := decode(t, out.stdout)
			if out.code != 2 || r.Error == nil || r.Error.Code != "invalid_input" ||
				r.Error.Message != `invalid value "" for flag -`+flag+": "+want {
				t.Errorf("%q --json: code=%d stdout=%q", name, out.code, out.stdout)
			}
			out = call{args: append([]string{"session", "search"}, args...)}.run(t)
			if out.code != 2 || out.stdout != "" || !strings.HasPrefix(out.stderr, "aits: ") || strings.Count(out.stderr, "\n") != 1 {
				t.Errorf("%q text: code=%d stdout=%q stderr=%q", name, out.code, out.stdout, out.stderr)
			}
		}
	}
}

// Text mode shows control characters from transcripts as Go escapes, so a
// transcript cannot drive the terminal; --json keeps the original text (P5).
func TestSessionSearchEscapesControls(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	writeTranscript(t, home, ".claude/projects/-w/a.jsonl", now,
		`{"type":"user","sessionId":"a","cwd":"/work/a\rFAKE","message":{"content":"before \u001b[2J\u001b]0;title\u0007 needle \u001b[31mred\u001b[0m"}}`)
	writeTranscript(t, home, ".codex/sessions/2026/10/14/rollout-x-b.jsonl", now.Add(-time.Hour),
		`{"type":"session_meta","payload":{"id":"abc\u001b[8mx","cwd":"/home/u/项目\\dir"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"needle \u009b \u007f"}]}}`)

	out := call{env: []string{"HOME=" + home}, args: []string{"session", "search", "needle"}}.run(t)
	if out.code != 0 || strings.ContainsAny(out.stdout, "\x1b\x07\r\x7f\u009b") {
		t.Fatalf("text output holds raw control characters: code=%d stdout=%q", out.code, out.stdout)
	}
	for _, want := range []string{
		`/work/a\rFAKE`, `before \x1b[2J\x1b]0;title\a needle \x1b[31mred\x1b[0m`,
		`abc\x1b[8mx`, `/home/u/项目\dir`, `needle \u009b \x7f`,
	} {
		if !strings.Contains(out.stdout, want) {
			t.Errorf("text output lacks %q:\n%s", want, out.stdout)
		}
	}

	o := decodeSearch(t, call{env: []string{"HOME=" + home}, args: []string{"session", "search", "--json", "needle"}}.run(t).stdout)
	if len(o.Results) != 2 || o.Results[0].Cwd != "/work/a\rFAKE" || o.Results[1].SessionID != "abc\x1b[8mx" ||
		o.Results[1].Snippet != "needle \u009b \x7f" {
		t.Errorf("--json changed the text: %+v", o.Results)
	}
}

// A prompt queued with a pasted image is searched by its words, never by the
// image's data (#23).
func TestSessionSearchQueuedBlocks(t *testing.T) {
	home := t.TempDir()
	writeTranscript(t, home, ".claude/projects/-w-proj/c1.jsonl", time.Now(),
		`{"type":"user","sessionId":"claude-q","cwd":"/w/proj","message":{"role":"user","content":"start the refactor"}}`,
		`{"type":"attachment","sessionId":"claude-q","cwd":"/w/proj","attachment":{"type":"queued_command","commandMode":"prompt","prompt":[{"type":"text","text":"blockqueued see this screenshot"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}],"origin":{"kind":"human"}}}`)
	out := call{env: []string{"HOME=" + home}, args: []string{"session", "search", "--json", "blockqueued"}}.run(t)
	r := decodeSearch(t, out.stdout)
	if out.code != 0 || len(r.Results) != 1 || r.Results[0].SessionID != "claude-q" || r.Results[0].Snippet != "blockqueued see this screenshot" ||
		r.Skipped.Files != 0 || r.Skipped.Lines != 0 {
		t.Errorf("blockqueued: code=%d stdout=%q", out.code, out.stdout)
	}
	out = call{env: []string{"HOME=" + home}, args: []string{"session", "search", "--json", "iVBORw0KGgo"}}.run(t)
	if out.code != 0 || out.stdout != noResults {
		t.Errorf("image data: code=%d stdout=%q", out.code, out.stdout)
	}
}

// A Codex rollout whose first line cannot be read is reported, never returned
// under the id of a later session_meta line (#23).
func TestSessionSearchCodexUnreadableFirstLine(t *testing.T) {
	home := t.TempDir()
	writeTranscript(t, home, ".codex/sessions/2026/10/14/rollout-c.jsonl", time.Now(),
		`{"type":"session_meta","payload":{"id":"sub-1","cwd":"/w/proj","source":{"subagent":{"thread_sp`,
		`{"type":"session_meta","payload":{"id":"parent-1","cwd":"/w/proj","source":"cli"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"subagentword review the diff"}]}}`)
	out := call{env: []string{"HOME=" + home}, args: []string{"session", "search", "--json", "subagentword"}}.run(t)
	if out.code != 0 || out.stdout != `{"results":[],"skipped":{"files":1,"lines":1}}`+"\n" {
		t.Errorf("--json: code=%d stdout=%q", out.code, out.stdout)
	}
	out = call{env: []string{"HOME=" + home}, args: []string{"session", "search", "subagentword"}}.run(t)
	if out.code != 0 || out.stdout != "" || out.stderr != "aits: skipped 1 files and 1 lines that could not be read\n" {
		t.Errorf("text: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
}

// codexRollout writes a Codex rollout whose first line is a session_meta
// with source ("" for none), and one user message.
func codexRollout(t *testing.T, home, id, cwd, source, text string, mtime time.Time) {
	t.Helper()
	if source != "" {
		source = `,"source":"` + source + `"`
	}
	writeTranscript(t, home, ".codex/sessions/2026/10/14/rollout-"+id+".jsonl", mtime,
		`{"type":"session_meta","payload":{"id":"`+id+`","cwd":"`+cwd+`"`+source+`}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"`+text+`"}]}}`)
}

// sourceTranscripts writes one session per source kind under a temporary
// HOME, each mentioning "migration": Codex rollouts whose session_meta source
// is cli, vscode, exec, mcp and missing, and Claude transcripts whose
// entrypoint is cli, sdk-cli and missing (#24).
func sourceTranscripts(t *testing.T) string {
	home := t.TempDir()
	at := time.Now().Add(-time.Hour)
	codex := func(id, cwd, source string) {
		codexRollout(t, home, id, cwd, source, "the migration fails", at)
		at = at.Add(time.Minute)
	}
	claude := func(id, entrypoint string) {
		writeTranscript(t, home, ".claude/projects/-w-app/"+id+".jsonl", at,
			`{"type":"user","sessionId":"`+id+`","cwd":"/w/app"`+entrypoint+`,"message":{"role":"user","content":"the migration fails"}}`)
		at = at.Add(time.Minute)
	}
	codex("codex-cli", "/w/app", "cli")
	codex("codex-vscode", "/w/other", "vscode")
	codex("codex-exec", "/w/app", "exec")
	codex("codex-mcp", "/w/app", "mcp")
	codex("codex-none", "/w/app", "")
	claude("claude-cli", `,"entrypoint":"cli"`)
	claude("claude-sdk", `,"entrypoint":"sdk-cli"`)
	claude("claude-none", ``)
	return home
}

func TestSessionSearchSource(t *testing.T) {
	home := sourceTranscripts(t)
	for _, c := range []struct {
		name string
		args []string
		want []string
	}{
		{"no filter", nil, []string{"claude:claude-cli", "claude:claude-none", "claude:claude-sdk", "codex:codex-cli", "codex:codex-exec", "codex:codex-mcp", "codex:codex-none", "codex:codex-vscode"}},
		{"interactive", []string{"--source", "interactive"}, []string{"claude:claude-cli", "codex:codex-cli", "codex:codex-vscode"}},
		{"scripted", []string{"--source", "scripted"}, []string{"claude:claude-sdk", "codex:codex-exec"}},
		{"with --agent", []string{"--source", "interactive", "--agent", "codex"}, []string{"codex:codex-cli", "codex:codex-vscode"}},
		{"with --cwd", []string{"--source", "interactive", "--cwd", "/w/app"}, []string{"claude:claude-cli", "codex:codex-cli"}},
		{"last value wins", []string{"--source", "scripted", "--source", "interactive"}, []string{"claude:claude-cli", "codex:codex-cli", "codex:codex-vscode"}},
	} {
		args := append(append([]string{"session", "search", "--json"}, c.args...), "migration")
		out := call{env: []string{"HOME=" + home}, args: args}.run(t)
		r := decodeSearch(t, out.stdout)
		got := found(r)
		slices.Sort(got)
		if out.code != 0 || !slices.Equal(got, c.want) || r.Skipped.Files != 0 || r.Skipped.Lines != 0 {
			t.Errorf("%s: code=%d got %q, want %q (skipped %+v)", c.name, out.code, got, c.want, r.Skipped)
		}
	}
}

// The filter runs before --limit cuts the newest results (#24).
func TestSessionSearchSourceBeforeLimit(t *testing.T) {
	home := t.TempDir()
	at := time.Now().Add(-time.Hour)
	for _, s := range []struct{ id, source string }{{"old-cli", "cli"}, {"exec-1", "exec"}, {"exec-2", "exec"}, {"exec-3", "exec"}} {
		codexRollout(t, home, s.id, "/w", s.source, "migration", at)
		at = at.Add(time.Minute)
	}
	out := call{env: []string{"HOME=" + home}, args: []string{"session", "search", "--json", "--source", "interactive", "--limit", "1", "migration"}}.run(t)
	if r := decodeSearch(t, out.stdout); out.code != 0 || len(r.Results) != 1 || r.Results[0].SessionID != "old-cli" {
		t.Errorf("code=%d stdout=%q", out.code, out.stdout)
	}
}

// A session must hold every term. The second session's thinking holds
// "alpha", so its file passes the prefilter and parsing must reject it (#22).
func TestSessionSearchAllTerms(t *testing.T) {
	home := t.TempDir()
	writeTranscript(t, home, ".claude/projects/-w/both.jsonl", time.Now(),
		claudeUser("both", "needle alpha"))
	writeTranscript(t, home, ".claude/projects/-w/one.jsonl", time.Now().Add(-time.Hour),
		claudeUser("one", "needle only"),
		`{"type":"assistant","sessionId":"one","cwd":"/w","message":{"content":[{"type":"thinking","thinking":"alpha"}]}}`)
	for _, c := range []struct{ terms, want []string }{
		{[]string{"needle", "alpha"}, []string{"claude:both"}},
		{[]string{"needle"}, []string{"claude:both", "claude:one"}},
	} {
		if got := found(searchJSON(t, []string{"HOME=" + home}, c.terms...)); !slices.Equal(got, c.want) {
			t.Errorf("%q: got %q, want %q", c.terms, got, c.want)
		}
	}
}

// Subagent text is never searched: a Claude subagent file, a Claude sidechain
// event and a Codex subagent rollout. None of them is counted as skipped (#22).
func TestSessionSearchSubagentsNotSearched(t *testing.T) {
	home := t.TempDir()
	writeTranscript(t, home, ".claude/projects/-w/main/subagents/agent-x.jsonl", time.Now(),
		claudeUser("main", "subagent-word"))
	writeTranscript(t, home, ".claude/projects/-w/side.jsonl", time.Now(),
		`{"type":"user","isSidechain":true,"sessionId":"side","cwd":"/w","message":{"role":"user","content":"subagent-word"}}`)
	writeTranscript(t, home, ".codex/sessions/2026/10/14/rollout-sub.jsonl", time.Now(),
		`{"type":"session_meta","payload":{"id":"sub","cwd":"/w","source":{"subagent":"review"}}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"subagent-word"}]}}`)
	out := call{env: []string{"HOME=" + home}, args: []string{"session", "search", "--json", "subagent-word"}}.run(t)
	if out.code != 0 || out.stdout != noResults || out.stderr != "" {
		t.Errorf("code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
}

// A file without a session id is counted and does not hide other results (#22).
func TestSessionSearchFileWithoutID(t *testing.T) {
	home := t.TempDir()
	writeTranscript(t, home, ".claude/projects/-w/noid.jsonl", time.Now(),
		`{"type":"user","message":{"content":"needle"}}`)
	writeTranscript(t, home, ".claude/projects/-w/good.jsonl", time.Now(),
		claudeUser("good", "needle"))
	o := searchJSON(t, []string{"HOME=" + home}, "needle")
	if !slices.Equal(found(o), []string{"claude:good"}) || o.Skipped.Files != 1 || o.Skipped.Lines != 0 {
		t.Errorf("--json: %+v", o)
	}
	out := call{env: []string{"HOME=" + home}, args: []string{"session", "search", "needle"}}.run(t)
	if out.code != 0 || !strings.Contains(out.stdout, "  claude  good  /w\n") ||
		out.stderr != "aits: skipped 1 files and 0 lines that could not be read\n" {
		t.Errorf("text: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
}

// A missing vendor directory is not an error and is not counted (#22).
func TestSessionSearchMissingVendorRoots(t *testing.T) {
	empty := []string{"HOME=" + t.TempDir()}
	out := call{env: empty, args: []string{"session", "search", "--json", "needle"}}.run(t)
	if out.code != 0 || out.stdout != noResults || out.stderr != "" {
		t.Errorf("neither, --json: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
	out = call{env: empty, args: []string{"session", "search", "needle"}}.run(t)
	if out.code != 0 || out.stdout != "" || out.stderr != "" {
		t.Errorf("neither, text: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
	home := t.TempDir()
	writeTranscript(t, home, ".claude/projects/-w/only.jsonl", time.Now(),
		claudeUser("only", "needle"))
	if o := searchJSON(t, []string{"HOME=" + home}, "needle"); !slices.Equal(found(o), []string{"claude:only"}) || o.Skipped.Files != 0 || o.Skipped.Lines != 0 {
		t.Errorf("claude only: %+v", o)
	}
}

// CLAUDE_CONFIG_DIR and CODEX_HOME replace the roots under HOME (#22).
func TestSessionSearchVendorRootsFromEnv(t *testing.T) {
	home, alt := t.TempDir(), t.TempDir()
	writeTranscript(t, alt, ".claude/projects/-w/env-claude.jsonl", time.Now(), claudeUser("env-claude", "needle"))
	codexRollout(t, alt, "env-codex", "/w", "", "needle", time.Now().Add(-time.Hour))
	writeTranscript(t, home, ".claude/projects/-w/home-claude.jsonl", time.Now(),
		claudeUser("home-claude", "needle"))
	codexRollout(t, home, "home-codex", "/w", "", "needle", time.Now())
	env := []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + filepath.Join(alt, ".claude"), "CODEX_HOME=" + filepath.Join(alt, ".codex")}
	for _, c := range []struct{ args, want []string }{
		{nil, []string{"claude:env-claude", "codex:env-codex"}},
		{[]string{"--agent", "claude"}, []string{"claude:env-claude"}},
		{[]string{"--agent", "codex"}, []string{"codex:env-codex"}},
	} {
		if got := found(searchJSON(t, env, append(c.args, "needle")...)); !slices.Equal(got, c.want) {
			t.Errorf("%q: got %q, want %q", c.args, got, c.want)
		}
	}
}
