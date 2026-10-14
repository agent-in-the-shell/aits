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
				outcomes[i] = examine(jobs[i], q.Terms, ns, dirs, b)
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
func examine(j job, terms []string, ns [][]byte, dirs []string, b *buffers) outcome {
	f, err := os.Open(j.path)
	if err != nil {
		return outcome{badFile: true}
	}
	defer f.Close()
	if ok, err := mayMatch(f, ns, b); err != nil {
		return outcome{badFile: true}
	} else if !ok {
		return outcome{}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return outcome{badFile: true}
	}
	t := newTranscript(terms)
	parse := vendors[j.agent].parse
	notSession, sawMessage := false, false
	bad, err := eachLine(f, b, func(line []byte) bool {
		p, ok := parse(line)
		t.identify(p.id, p.cwd)
		notSession = notSession || p.notSession
		sawMessage = sawMessage || p.message || len(p.texts) > 0
		for _, s := range p.texts {
			t.add(s)
		}
		return ok
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
	inDir := len(dirs) == 0 || slices.ContainsFunc(dirs, func(d string) bool { return within(t.cwd, d) })
	if !t.matched() || !inDir {
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
func eachLine(r io.Reader, b *buffers, fn func([]byte) bool) (int, error) {
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
		part, err := b.reader.ReadSlice('\n')
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
