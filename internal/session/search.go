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

	"github.com/agent-in-the-shell/aits/internal/store"
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
	kinds    map[string]string // a recorded source value: "interactive" or "scripted"
}

var vendors = map[string]vendor{
	"claude": {rootVar: "CLAUDE_CONFIG_DIR", rootHome: ".claude", pattern: "projects/*/*.jsonl", parse: parseClaude,
		kinds: map[string]string{
			"cli": "interactive", "claude-vscode": "interactive", "claude-desktop": "interactive",
			"sdk-cli": "scripted", "sdk-ts": "scripted", "sdk-py": "scripted",
		}},
	"codex": {rootVar: "CODEX_HOME", rootHome: ".codex", pattern: "sessions/*/*/*/rollout-*.jsonl", parse: parseCodex,
		kinds: map[string]string{"cli": "interactive", "vscode": "interactive", "exec": "scripted"}},
}

// root is the vendor's transcript directory: its own variable, else under
// HOME. With neither set it cannot be known, which is an error, not an empty
// search (P6).
func (v vendor) root(environ []string) (string, error) {
	if root := store.Lookup(environ, v.rootVar); root != "" {
		return root, nil
	}
	if home := store.Lookup(environ, "HOME"); home != "" {
		return filepath.Join(home, v.rootHome), nil
	}
	return "", invalid("neither %s nor HOME is set", v.rootVar)
}

// files lists the transcript files under root. A missing root lists nothing.
func (v vendor) files(root string) []string {
	// The pattern is fixed, so Glob cannot fail; it skips unreadable
	// directories without saying so (a documented limit).
	paths, _ := filepath.Glob(filepath.Join(escapeGlob(root), v.pattern))
	return paths
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
	for _, name := range q.agents() {
		v := vendors[name]
		root, err := v.root(q.Environ) // validate checked it; never glob a relative pattern
		if err != nil {
			return Output{}, err
		}
		for _, p := range v.files(root) {
			fi, err := os.Stat(p) // follows symlinks: the target is what would be read
			if err != nil {
				out.Skipped.Files++
				continue
			}
			if fi.ModTime().Before(cutoff) {
				continue
			}
			if !fi.Mode().IsRegular() {
				// Never opened: a pipe would block the search and a device
				// would never end it (P6: counted, not dropped).
				out.Skipped.Files++
				continue
			}
			jobs = append(jobs, job{agent: name, path: p, mtime: fi.ModTime()})
		}
	}

	ns := needles(q.Terms)
	dirs := cwdForms(q.Cwd)
	outcomes := make([]outcome, len(jobs))
	next := make(chan int)
	var wg sync.WaitGroup
	for range runtime.NumCPU() {
		wg.Go(func() {
			b := newBuffers()
			for i := range next {
				outcomes[i] = examine(ctx, jobs[i], q.Terms, q.Source, ns, dirs, b)
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

// examine prefilters one file and, if every term may occur, parses it. It
// stops between chunks and lines once ctx is cancelled; Search then reports
// the cancellation instead of any outcome.
func examine(ctx context.Context, j job, terms []string, source string, ns [][]byte, dirs []string, b *buffers) outcome {
	f, err := os.Open(j.path)
	if err != nil {
		return outcome{badFile: true}
	}
	defer f.Close()
	if ok, err := mayMatch(ctx, f, ns, b); err != nil {
		return outcome{badFile: true}
	} else if !ok {
		return outcome{}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return outcome{badFile: true}
	}
	t := newTranscript(terms)
	v := vendors[j.agent]
	inDir := func() bool {
		return len(dirs) == 0 || slices.ContainsFunc(dirs, func(d string) bool { return within(t.cwd, d) })
	}
	// A session of unknown source (no value, or one not in kinds) passes no
	// source filter: its kind cannot be told (P6).
	kindOK := func() bool { return source == "" || v.kinds[t.source] == source }
	notSession, sawMessage, first := false, false, true
	bad, err := eachLine(ctx, f, b, func(line []byte) (bool, bool) {
		p, ok := v.parse(line, first)
		first = false
		t.identify(p)
		notSession = notSession || p.notSession
		sawMessage = sawMessage || p.message || len(p.texts) > 0
		for _, s := range p.texts {
			t.add(s)
		}
		// The cwd and source are first values: once the session is named and
		// either rules it out, no later line can make it a result, so its
		// later lines are not read, nor counted in skipped.lines (#51).
		excluded := t.sessionID != "" && t.cwd != "" && (!inDir() || t.source != "" && !kindOK())
		return ok, !excluded
	})
	switch {
	case err != nil:
		return outcome{lines: bad, badFile: true}
	case notSession:
		return outcome{lines: bad} // a subagent's rollout: not a session to return
	case t.cwd == "" && !sawMessage && bad == 0:
		return outcome{} // a readable file that holds no conversation, such as metadata
	case t.sessionID == "" || t.cwd == "":
		return outcome{lines: bad, badFile: true}
	}
	if !t.matched() || !inDir() || !kindOK() {
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
// were unusable: fn returned ok false, or the line exceeded maxLine. A line
// over maxLine is passed as nil, so that fn still sees each line's place (the
// first line matters to a parser); it counts as unusable whatever fn
// returns. It stops after a line for which fn returns more false, and returns
// ctx's error as soon as ctx is cancelled, between lines.
func eachLine(ctx context.Context, r io.Reader, b *buffers, fn func(line []byte) (ok, more bool)) (int, error) {
	b.reader.Reset(r)
	line := b.line[:0]
	defer func() {
		if cap(line) > chunkSize {
			line = nil // do not hold a huge line's buffer for the rest of the search
		}
		b.line = line[:0]
	}()
	bad := 0
	tooLong := false
	for {
		if err := ctx.Err(); err != nil {
			return bad, err
		}
		part, err := b.reader.ReadSlice('\n')
		if tooLong || len(line)+len(part) > maxLine {
			tooLong = true
		} else {
			line = append(line, part...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		more := true
		switch l := bytes.TrimSpace(line); {
		case tooLong:
			_, more = fn(nil)
			bad++
		case len(l) > 0:
			var ok bool
			if ok, more = fn(l); !ok {
				bad++
			}
		}
		line, tooLong = line[:0], false
		if err == io.EOF || !more {
			return bad, nil
		}
		if err != nil {
			return bad, err
		}
	}
}

// cwdForms returns the directories a session may be in for --cwd: none (any)
// for "", else dir as given and, if different, with symlinks resolved. Agents
// usually record the resolved path (on macOS /tmp is /private/tmp), but not
// always.
func cwdForms(dir string) []string {
	if dir == "" {
		return nil
	}
	forms := []string{dir}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != filepath.Clean(dir) {
		forms = append(forms, real)
	}
	return forms
}

// within reports whether dir is base or below it.
func within(dir, base string) bool {
	dir, base = filepath.Clean(dir), filepath.Clean(base)
	return dir == base || strings.HasPrefix(dir, strings.TrimSuffix(base, "/")+"/")
}
