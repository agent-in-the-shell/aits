package job

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/agent-in-the-shell/aits/internal/shell"
)

// crontabTimeout bounds each crontab command apply runs.
const crontabTimeout = 30 * time.Second

// Spec is the file job apply reads: the jobs to install and the PATH they
// start with (#133).
type Spec struct {
	Path string    `json:"path"`
	Jobs []JobSpec `json:"jobs"`
}

// JobSpec is one declared job: when it runs, and the job run line for it.
type JobSpec struct {
	Name     string   `json:"name"`
	Schedule string   `json:"schedule"`
	Timeout  string   `json:"timeout"`
	OnAlert  string   `json:"on_alert"`
	Env      []string `json:"env"`
	Hosts    []string `json:"hosts"`
	Command  []string `json:"command"`
}

// ApplyRequest is one job apply.
type ApplyRequest struct {
	File    string // the spec; relative to aits's working directory
	Aits    string // the absolute path of aits, which each line runs
	DryRun  bool
	Sched   bool     // run the jobs from one LaunchAgent's job sched, not cron (macOS)
	Environ []string // the caller's environment: HOME for ~/, PATH for crontab
}

// Applied is what an apply changed, or would change, by job name.
type Applied struct {
	Host      string        `json:"host"`
	Added     []string      `json:"added"`
	Changed   []string      `json:"changed"`
	Removed   []string      `json:"removed"`
	Unchanged []string      `json:"unchanged"`
	Sched     *SchedApplied `json:"sched,omitempty"` // on macOS, once --sched was used for the file
}

// Apply installs the jobs of r.File declared for this host into the user's
// crontab, in the block apply owns for that file, and removes the ones no
// longer declared. Every other line of the crontab is kept as it is. With
// r.Sched, on macOS, it installs instead a LaunchAgent that runs job sched
// for the file, and removes the block; without it, it removes that agent. A
// non-nil error means nothing was changed, except when the agent was
// installed and the crontab then failed.
func Apply(ctx context.Context, r ApplyRequest) (Applied, error) {
	file, err := filepath.Abs(r.File)
	if err != nil {
		return Applied{}, invalid("spec %q: %v", r.File, err)
	}
	if !filepath.IsAbs(r.Aits) {
		return Applied{}, invalid("aits %q is not an absolute path", r.Aits)
	}
	if r.Sched && runtime.GOOS != "darwin" {
		return Applied{}, invalid("--sched installs a LaunchAgent, so it works only on macOS for now")
	}
	spec, err := readSpec(file)
	if err != nil {
		return Applied{}, err
	}
	host, err := os.Hostname()
	if err != nil {
		return Applied{}, fmt.Errorf("host name: %w", err)
	}
	host, _, _ = strings.Cut(host, ".")
	home, _ := lookup(r.Environ, "HOME")
	path, jobs, err := plan(spec, host, home)
	if err != nil {
		return Applied{}, err
	}
	var names, lines []string
	if !r.Sched {
		for _, j := range jobs {
			names = append(names, j.name)
		}
		if lines, err = render(path, jobs, r.Aits); err != nil {
			return Applied{}, err
		}
	}
	for _, s := range append([]string{file}, lines...) {
		if strings.ContainsAny(s, "\n\r\x00") {
			return Applied{}, invalid("a line for the crontab cannot hold a line break or NUL: %q", s)
		}
	}

	env := shell.Environ(r.Environ, nil)
	crontab, err := shell.LookPath(env, "crontab")
	if err != nil {
		return Applied{}, errors.New("crontab is not on PATH")
	}
	table, err := readCrontab(ctx, crontab, env)
	if err != nil {
		return Applied{}, err
	}
	begin, end := "# BEGIN aits job apply "+file, "# END aits job apply "+file
	before, old, after, err := cut(table, begin, end)
	if err != nil {
		return Applied{}, err
	}
	installed := map[string]string{}
	var gone []string
	for _, line := range old {
		name := lineName(line)
		if name == "" || installed[name] != "" {
			return Applied{}, invalid("the crontab's block for %s has a line apply did not write: %q; fix it with crontab -e", file, line)
		}
		installed[name] = line
		gone = append(gone, name)
	}
	res := Applied{Host: host, Added: []string{}, Changed: []string{}, Removed: []string{}, Unchanged: []string{}}
	for i, name := range names {
		switch prev, ok := installed[name]; {
		case !ok:
			res.Added = append(res.Added, name)
		case prev != lines[i]:
			res.Changed = append(res.Changed, name)
		default:
			res.Unchanged = append(res.Unchanged, name)
		}
	}
	for _, name := range gone {
		if !slices.Contains(names, name) {
			res.Removed = append(res.Removed, name)
		}
	}
	want := slices.Clone(before)
	if len(lines) > 0 {
		want = append(want, begin)
		want = append(want, lines...)
		want = append(want, end)
	}
	want = append(want, after...)

	// The agent: what is installed, and what this apply wants.
	var ag agent
	var have, text string
	if runtime.GOOS == "darwin" {
		if ag, err = newAgent(file, home, env); err != nil && r.Sched {
			return Applied{}, err
		}
		if err == nil {
			if have, err = ag.read(); err != nil {
				return Applied{}, err
			}
		}
		if r.Sched {
			dir, err := jobDir(r.Environ)
			if err != nil {
				return Applied{}, err
			}
			aitsHome, _ := lookup(r.Environ, "AITS_HOME")
			text = ag.render(r.Aits, file, SchedLog(dir, file), aitsHome)
		}
		if have != "" || r.Sched {
			res.Sched = &SchedApplied{Label: ag.label, Plist: ag.plist}
			switch {
			case !r.Sched:
				res.Sched.Change = "removed"
			case have == "":
				res.Sched.Change = "added"
			case have != text:
				res.Sched.Change = "changed"
			default:
				res.Sched.Change = "unchanged"
			}
		}
	}
	if r.DryRun {
		return res, nil
	}
	// One file runs one way: start the agent before removing the block, and
	// install the block before removing the agent.
	if r.Sched && (have != text || !ag.loaded(ctx)) {
		if err := ag.install(ctx, text); err != nil {
			return Applied{}, err
		}
	}
	if !slices.Equal(want, table) {
		if err := writeCrontab(ctx, crontab, env, want); err != nil {
			return Applied{}, err
		}
		got, err := readCrontab(ctx, crontab, env)
		if err != nil {
			return Applied{}, err
		}
		if !slices.Equal(got, want) {
			return Applied{}, errors.New("crontab did not install the table apply gave it; check it with crontab -l")
		}
	}
	if !r.Sched && have != "" {
		if err := ag.remove(ctx); err != nil {
			return Applied{}, err
		}
	}
	return res, nil
}

// readSpec reads and decodes file, refusing a key it does not know (P6).
func readSpec(file string) (Spec, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return Spec{}, invalid("spec: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var spec Spec
	if err := dec.Decode(&spec); err != nil {
		return Spec{}, invalid("spec %s: %v", file, err)
	}
	if dec.More() {
		return Spec{}, invalid("spec %s: more than one JSON value", file)
	}
	return spec, nil
}

// planned is one job of a spec declared for this host, checked, with its
// paths expanded.
type planned struct {
	name     string
	schedule string // its fields joined by one space
	cron     cron
	timeout  time.Duration // 0: job run's default
	given    string        // the timeout as the spec gives it
	env      []string
	onAlert  string
	command  []string
}

// plan checks every job of spec and returns the PATH entries of the spec and
// the jobs declared for host, in the spec's order.
func plan(spec Spec, host, home string) ([]string, []planned, error) {
	expand := func(what, p string) (string, error) {
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			if home == "" {
				return "", invalid("%s %q starts with ~/ but HOME is not set", what, p)
			}
			p = filepath.Join(home, rest)
		}
		return p, nil
	}
	var path []string
	if spec.Path != "" {
		for _, dir := range filepath.SplitList(spec.Path) {
			dir, err := expand("path entry", dir)
			if err != nil {
				return nil, nil, err
			}
			if !filepath.IsAbs(dir) {
				return nil, nil, invalid("path entry %q is not an absolute path", dir)
			}
			path = append(path, dir)
		}
	}
	var jobs []planned
	seen := map[string]bool{}
	for _, j := range spec.Jobs {
		if !validName(j.Name) {
			return nil, nil, invalid("job name %q must be letters, digits, '.', '_' or '-', start with a letter or digit, and be at most 100 bytes", j.Name)
		}
		if seen[j.Name] {
			return nil, nil, invalid("job %s is declared more than once", j.Name)
		}
		seen[j.Name] = true
		bad := func(format string, a ...any) error {
			return invalid("job %s: %s", j.Name, fmt.Sprintf(format, a...))
		}
		c, err := parseCron(j.Schedule)
		if err != nil {
			return nil, nil, bad("schedule %q: %v", j.Schedule, err)
		}
		p := planned{name: j.Name, schedule: strings.Join(strings.Fields(j.Schedule), " "), cron: c, env: j.Env}
		if len(j.Command) == 0 {
			return nil, nil, bad("no command")
		}
		if p.given = j.Timeout; j.Timeout != "" {
			if p.timeout, err = time.ParseDuration(j.Timeout); err != nil || p.timeout <= 0 {
				return nil, nil, bad("timeout %q is not a positive duration such as 30m", j.Timeout)
			}
		}
		for _, name := range j.Env {
			if name == "" || strings.Contains(name, "=") {
				return nil, nil, bad("%q is not an environment variable name", name)
			}
		}
		if slices.Contains(j.Hosts, "") {
			return nil, nil, bad("an empty host name")
		}
		if len(j.Hosts) > 0 && !slices.Contains(j.Hosts, host) {
			continue // its paths are its own host's to check
		}
		p.command = slices.Clone(j.Command)
		if p.command[0], err = expand("command", p.command[0]); err != nil {
			return nil, nil, err
		}
		if err := executable(p.command[0]); err != nil {
			return nil, nil, bad("command %v", err)
		}
		if j.OnAlert != "" {
			if p.onAlert, err = expand("on_alert", j.OnAlert); err != nil {
				return nil, nil, err
			}
			if err := executable(p.onAlert); err != nil {
				return nil, nil, bad("on_alert %v", err)
			}
		}
		jobs = append(jobs, p)
	}
	return path, jobs, nil
}

// render returns the crontab line of each job: aits's job run with the
// job's flags, after PATH from path.
func render(path []string, jobs []planned, aits string) ([]string, error) {
	var lines []string
	for _, j := range jobs {
		args := []string{aits, "job", "run", j.name}
		if j.given != "" {
			args = append(args, "--timeout", j.given)
		}
		for _, name := range j.env {
			args = append(args, "--env", name)
		}
		if j.onAlert != "" {
			args = append(args, "--on-alert", j.onAlert)
		}
		args = append(append(args, "--"), j.command...)
		var b strings.Builder
		b.WriteString(j.schedule + " ")
		if path != nil {
			b.WriteString(quote("PATH="+strings.Join(path, string(filepath.ListSeparator))) + " ")
		}
		for i, a := range args {
			if strings.Contains(a, "%") {
				// cron turns % into a line break.
				return nil, invalid("job %s: %q holds a %%, which cron reads as a line break", j.name, a)
			}
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(quote(a))
		}
		lines = append(lines, b.String())
	}
	return lines, nil
}

// executable checks that p is an absolute path to an executable file.
func executable(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("%q is not an absolute path or ~/", p)
	}
	fi, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("%q: %v", p, err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%q is not an executable file", p)
	}
	return nil
}

// quote quotes s for sh when it holds anything but plain characters. An
// assignment such as PATH=... is quoted after its '=', so sh still reads it
// as one.
func quote(s string) string {
	plain := func(s string) bool {
		return s != "" && !strings.ContainsFunc(s, func(c rune) bool { return !isAlnum(c) && !strings.ContainsRune("_@+=:,./-", c) })
	}
	if plain(s) {
		return s
	}
	if name, value, ok := strings.Cut(s, "="); ok && plain(name) {
		return name + "=" + quote(value)
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// lineName returns the job a line apply wrote runs, or "".
func lineName(line string) string {
	_, rest, ok := strings.Cut(line, " job run ")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, " ")
	if !validName(name) {
		return ""
	}
	return name
}

// cut splits table around the block between the lines begin and end: the
// lines before it, inside it and after it. A table without the block is all
// before. A begin without its end, or a second block, is an error.
func cut(table []string, begin, end string) (before, block, after []string, err error) {
	b, e := slices.Index(table, begin), slices.Index(table, end)
	switch {
	case b < 0 && e < 0:
		return table, nil, nil, nil
	case e < 0:
		return nil, nil, nil, invalid("the crontab has %q without the line that closes it; fix it with crontab -e", begin)
	case b < 0 || e < b:
		return nil, nil, nil, invalid("the crontab has %q without the line that opens it; fix it with crontab -e", end)
	case slices.Contains(table[e+1:], begin) || slices.Contains(table[e+1:], end) || slices.Contains(table[b+1:e], begin):
		return nil, nil, nil, invalid("the crontab has the block for this file more than once; fix it with crontab -e")
	}
	return table[:b], table[b+1 : e], table[e+1:], nil
}

// readCrontab returns the user's crontab, one line per element; a user
// without one has an empty table.
func readCrontab(ctx context.Context, crontab string, env []string) ([]string, error) {
	var stdout, stderr bytes.Buffer
	if err := runCrontab(ctx, crontab, env, nil, &stdout, &stderr, "-l"); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && stdout.Len() == 0 && strings.Contains(stderr.String(), "no crontab") {
			return nil, nil
		}
		return nil, fmt.Errorf("crontab -l: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	s := strings.TrimSuffix(stdout.String(), "\n")
	if s == "" {
		return nil, nil
	}
	return strings.Split(s, "\n"), nil
}

// writeCrontab installs table as the user's crontab.
func writeCrontab(ctx context.Context, crontab string, env, table []string) error {
	text := ""
	if len(table) > 0 {
		text = strings.Join(table, "\n") + "\n"
	}
	var stderr bytes.Buffer
	if err := runCrontab(ctx, crontab, env, strings.NewReader(text), io.Discard, &stderr, "-"); err != nil {
		return fmt.Errorf("crontab -: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func runCrontab(ctx context.Context, crontab string, env []string, stdin io.Reader, stdout, stderr io.Writer, arg string) error {
	ctx, cancel := context.WithTimeout(ctx, crontabTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, crontab, arg)
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, stdin, stdout, stderr
	return cmd.Run()
}

// lookup returns the value of name in environ.
func lookup(environ []string, name string) (string, bool) {
	for _, kv := range slices.Backward(environ) {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v, true
		}
	}
	return "", false
}
