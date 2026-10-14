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
