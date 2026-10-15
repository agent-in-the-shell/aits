package session

import (
	"context"
	"errors"
	"io"
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
	return h.writeFrom(rel, mtime, strings.NewReader(strings.Join(lines, "\n")+"\n"))
}

// writeFrom writes r to rel, so that a large file need not be built in memory.
func (h home) writeFrom(rel string, mtime time.Time, r io.Reader) string {
	h.t.Helper()
	p := filepath.Join(h.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		h.t.Fatal(err)
	}
	f, err := os.Create(p)
	if err == nil {
		_, err = io.Copy(f, r)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err == nil {
		err = os.Chtimes(p, mtime, mtime)
	}
	if err != nil {
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
		"no terms":       func(q *Query) { q.Terms = nil },
		"empty term":     func(q *Query) { q.Terms = []string{""} },
		"unknown agent":  func(q *Query) { q.Agent = "nope" },
		"relative cwd":   func(q *Query) { q.Cwd = "rel" },
		"negative since": func(q *Query) { q.Since = -time.Second },
		"zero limit":     func(q *Query) { q.Limit = 0 },
		"unknown source": func(q *Query) { q.Source = "both" },
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

// Files that are not conversations the user can continue are left out
// without being counted as unreadable: a Codex subagent's rollout, and a
// Claude metadata file with no messages and no cwd.
func TestSearchIgnoresNonSessions(t *testing.T) {
	h := newHome(t)
	now := time.Now()
	h.write(".codex/sessions/2026/10/14/rollout-x-sub.jsonl", now,
		`{"type":"session_meta","payload":{"id":"sub","cwd":"/w","source":{"subagent":{"thread_spawn":{}}}}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"needle"}]}}`)
	h.write(".claude/projects/-w/meta.jsonl", now, `{"type":"bridge","sessionId":"m","bridgeSessionId":"needle"}`)
	h.write(".claude/projects/-w/real.jsonl", now, claudeLine("real", "/w", "user", "needle"))
	out := search(t, Query{Terms: []string{"needle"}, Environ: h.env()})
	if got := ids(out); !slices.Equal(got, []string{"claude:real"}) || out.Skipped != (Skipped{}) {
		t.Errorf("results %q, skipped %+v", got, out.Skipped)
	}
}

// A transcript whose every line is unreadable is reported, not mistaken for a
// file without a conversation (P6).
func TestSearchCountsCorruptFile(t *testing.T) {
	h := newHome(t)
	h.write(".claude/projects/-w/broken.jsonl", time.Now(), `{"type":"user" needle`, `needle not json`)
	out := search(t, Query{Terms: []string{"needle"}, Environ: h.env()})
	if len(out.Results) != 0 || out.Skipped != (Skipped{Files: 1, Lines: 2}) {
		t.Errorf("results %q, skipped %+v", ids(out), out.Skipped)
	}
}

// A file with messages but no cwd is reported even when every message is text
// that is not searched: it is a conversation aits could not place (P6).
func TestSearchCountsSessionWithoutCwd(t *testing.T) {
	h := newHome(t)
	h.write(".claude/projects/-w/nocwd.jsonl", time.Now(),
		`{"type":"user","sessionId":"x","message":{"content":"<system-reminder>needle</system-reminder>"}}`)
	out := search(t, Query{Terms: []string{"needle"}, Environ: h.env()})
	if len(out.Results) != 0 || out.Skipped != (Skipped{Files: 1}) {
		t.Errorf("results %q, skipped %+v", ids(out), out.Skipped)
	}
}

// A Codex rollout takes its identity from its first line only: when that line
// cannot be read (here not JSON, or over maxLine), a later session_meta does
// not name the file, which is reported and never returned (P6, #23).
func TestSearchCodexIdentityFromFirstLine(t *testing.T) {
	later := "\n" + strings.Join(codexLines("parent-1", "/w", "subagentword review"), "\n") + "\n"
	firsts := map[string]func() io.Reader{
		"not JSON": func() io.Reader {
			return strings.NewReader(`{"type":"session_meta","payload":{"id":"sub-1","cwd":"/w","source":{"subagent":{"thread_sp` + later)
		},
		"too long": func() io.Reader { // streamed: a 64 MiB string would cost several copies
			return io.MultiReader(strings.NewReader(`{"type":"session_meta","payload":{"id":"sub-1","cwd":"/w","x":"`),
				io.LimitReader(repeatA{}, maxLine), strings.NewReader(`"}}`+later))
		},
	}
	for name, first := range firsts {
		t.Run(name, func(t *testing.T) {
			h := newHome(t)
			h.writeFrom(".codex/sessions/2026/10/14/rollout-c.jsonl", time.Now(), first())
			out := search(t, Query{Terms: []string{"subagentword"}, Environ: h.env()})
			if len(out.Results) != 0 || out.Skipped != (Skipped{Files: 1, Lines: 1}) {
				t.Errorf("results %q, skipped %+v", ids(out), out.Skipped)
			}
		})
	}
}

// repeatA reads as an endless run of 'a'.
type repeatA struct{}

func (repeatA) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

// A session's source is the first value its transcript records, classified
// per vendor; a missing, unknown or non-string value is "" (#24).
func TestSourceKinds(t *testing.T) {
	const head = `"sessionId":"s","cwd":"/w"`
	claude := map[string]string{
		`{"type":"user",` + head + `,"entrypoint":"cli"}`:            "interactive",
		`{"type":"user",` + head + `,"entrypoint":"claude-vscode"}`:  "interactive",
		`{"type":"user",` + head + `,"entrypoint":"claude-desktop"}`: "interactive",
		`{"type":"user",` + head + `,"entrypoint":"sdk-cli"}`:        "scripted",
		`{"type":"user",` + head + `,"entrypoint":"sdk-ts"}`:         "scripted",
		`{"type":"user",` + head + `,"entrypoint":"sdk-py"}`:         "scripted",
		`{"type":"user",` + head + `,"entrypoint":"remote"}`:         "",
		`{"type":"user",` + head + `,"entrypoint":7}`:                "",
		`{"type":"user",` + head + `}`:                               "",
	}
	codex := map[string]string{
		`{"type":"session_meta","payload":{"id":"s","cwd":"/w","source":"cli"}}`:            "interactive",
		`{"type":"session_meta","payload":{"id":"s","cwd":"/w","source":"vscode"}}`:         "interactive",
		`{"type":"session_meta","payload":{"id":"s","cwd":"/w","source":"exec"}}`:           "scripted",
		`{"type":"session_meta","payload":{"id":"s","cwd":"/w","source":"mcp"}}`:            "",
		`{"type":"session_meta","payload":{"id":"s","cwd":"/w","source":{"subagent":"x"}}}`: "",
		`{"type":"session_meta","payload":{"id":"s","cwd":"/w"}}`:                           "",
	}
	for agent, lines := range map[string]map[string]string{"claude": claude, "codex": codex} {
		v := vendors[agent]
		for line, want := range lines {
			p, ok := v.parse([]byte(line), true)
			if got := v.kinds[p.source]; !ok || got != want {
				t.Errorf("%s %s: kind %q (source %q, ok %v), want %q", agent, line, got, p.source, ok, want)
			}
		}
	}
}

// The first recorded source decides: an interactive Claude session later
// continued with claude -p stays interactive (#24).
func TestSourceFirstValue(t *testing.T) {
	h := newHome(t)
	h.write(".claude/projects/-w/c.jsonl", time.Now(),
		`{"type":"user","sessionId":"c","cwd":"/w","entrypoint":"cli","message":{"content":"migration"}}`,
		`{"type":"user","sessionId":"c","cwd":"/w","entrypoint":"sdk-cli","message":{"content":"migration again"}}`)
	for source, want := range map[string]int{"interactive": 1, "scripted": 0} {
		if out := search(t, Query{Terms: []string{"migration"}, Source: source, Environ: h.env()}); len(out.Results) != want {
			t.Errorf("--source %s: %q, want %d results", source, ids(out), want)
		}
	}
}

// A file that --cwd or --source rules out is read no further, so an
// unreadable line after the deciding one is not counted. Until the session
// id, cwd and (for --source) source are known, reading goes on (#51).
func TestSearchStopsReadingExcludedFile(t *testing.T) {
	h := newHome(t)
	h.write(".claude/projects/-other/c.jsonl", time.Now(),
		`{"type":"user","sessionId":"c","cwd":"/other","entrypoint":"sdk-cli","message":{"content":"needle"}}`,
		`needle not json`)
	h.write(".codex/sessions/2026/10/14/rollout-x.jsonl", time.Now(),
		`{"type":"session_meta","payload":{"id":"x","cwd":"/other","source":"exec"}}`,
		codexLines("x", "/other", "needle")[1],
		`needle not json`)
	late := newHome(t) // the source comes after a bad line
	late.write(".claude/projects/-w/late.jsonl", time.Now(),
		claudeLine("late", "/w", "user", "needle"),
		`needle not json`,
		`{"type":"user","sessionId":"late","cwd":"/w","entrypoint":"sdk-cli","message":{"content":"needle"}}`)
	noID := newHome(t)
	noID.write(".claude/projects/-other/noid.jsonl", time.Now(),
		`{"type":"user","cwd":"/other","message":{"content":"needle"}}`,
		`needle not json`)
	for _, c := range []struct {
		name    string
		h       home
		q       Query
		results int
		want    Skipped
	}{
		{"no filter", h, Query{}, 2, Skipped{Lines: 2}},
		{"--cwd", h, Query{Cwd: "/w"}, 0, Skipped{}},
		{"--source", h, Query{Source: "interactive"}, 0, Skipped{}},
		{"source not yet known", late, Query{Source: "interactive"}, 0, Skipped{Lines: 1}},
		{"id never known", noID, Query{Cwd: "/w"}, 0, Skipped{Files: 1, Lines: 1}},
	} {
		c.q.Terms, c.q.Environ = []string{"needle"}, c.h.env()
		if out := search(t, c.q); len(out.Results) != c.results || out.Skipped != c.want {
			t.Errorf("%s: results %q, skipped %+v; want %d results, %+v", c.name, ids(out), out.Skipped, c.results, c.want)
		}
	}
}
