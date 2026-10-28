// Package job is the "job" command group: it runs one scheduled command the
// way aits runs an agent (P10) — in its own process group, with a timeout, a
// lock against an overlapping run, a bounded log and a run record — and
// decides when its failures are worth an alert, which a hook the user owns
// delivers (#129). When a job runs is the OS scheduler's (#109).
package job

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/agent-in-the-shell/aits/internal/shell"
	"github.com/agent-in-the-shell/aits/internal/store"
)

// Status says how a run ended (P6: always explicit).
type Status string

const (
	StatusOK          Status = "ok"
	StatusFailed      Status = "failed"
	StatusTimeout     Status = "timeout"
	StatusSkipped     Status = "skipped"
	StatusStopped     Status = "stopped"
	StatusStartFailed Status = "start_failed"
)

// Alert is what a run decided to tell the hook.
type Alert string

const (
	AlertNone       Alert = "none"       // nothing to tell: a success after a success, a skipped or stopped run
	AlertFailure    Alert = "failure"    // a first failure, or one that differs from the last
	AlertRepeat     Alert = "repeat"     // the same failure again, once its backoff has passed
	AlertSuppressed Alert = "suppressed" // the same failure again, within its backoff
	AlertRecovery   Alert = "recovery"   // the first success after a failure
)

const (
	// killGrace is the time between SIGTERM and SIGKILL to the group (P10).
	killGrace = 5 * time.Second
	// hookTimeout bounds the alert hook.
	hookTimeout = 30 * time.Second
	// logMax is the size at which the log rotates, before a run; logKeep
	// rotated logs are kept (P8: bounded output).
	logMax  = 5 << 20
	logKeep = 3
	// tailSize is how much of the run's output an alert carries.
	tailSize = 4 << 10
	// signatureSize is how much of the tail tells two failures apart.
	signatureSize = 1500
)

// backoff is the time after the nth alert of one failure (n from 1) before
// it is repeated; after the last, backoffMax.
var backoff = []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour}

const backoffMax = 6 * time.Hour

// Request is one run of a job.
type Request struct {
	Name    string   // the job; see validName
	Command []string // the program and its arguments
	Timeout time.Duration
	Environ []string // the caller's environment; the command gets shell.Environ of it
	Pass    []string // variable names passed beyond the allowlist (P8)
	OnAlert string   // absolute path of the alert hook; empty: no hook
	Kill    <-chan struct{}
}

// Record is how one run ended.
type Record struct {
	Name       string    `json:"name"`
	Status     Status    `json:"status"`
	ExitCode   *int      `json:"exit_code,omitempty"` // set once the command ran; -1 if a signal ended it
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	DurationMS int64     `json:"duration_ms"`
	Message    string    `json:"message,omitempty"`
	Alert      Alert     `json:"alert"`
	// AlertError says why the hook failed to deliver the alert.
	AlertError string `json:"alert_error,omitempty"`
}

// State is what aits keeps about a job between runs.
type State struct {
	Name     string    `json:"name"`
	Last     *Record   `json:"last"` // the last run that was not skipped
	LastOKAt time.Time `json:"last_ok_at,omitzero"`
	Failing  *Failing  `json:"failing,omitempty"` // set while the job fails
}

// Failing is a job's current run of failures.
type Failing struct {
	Since       time.Time `json:"since"`     // when the first of them finished
	Failures    int       `json:"failures"`  // failed runs since then
	Signature   string    `json:"signature"` // of the last alerted failure
	Alerts      int       `json:"alerts"`    // alerts sent for that signature
	LastAlertAt time.Time `json:"last_alert_at"`
	Unalerted   int       `json:"unalerted"` // failed runs since the last alert
}

// Notice is the alert the hook gets on stdin.
type Notice struct {
	Name         string    `json:"name"`
	Kind         Alert     `json:"kind"`
	Status       Status    `json:"status"`
	ExitCode     *int      `json:"exit_code,omitempty"`
	Message      string    `json:"message,omitempty"`
	FinishedAt   time.Time `json:"finished_at"`
	FailingSince time.Time `json:"failing_since"`
	Failures     int       `json:"failures"`  // failed runs since failing_since, this one included unless it recovered
	Unalerted    int       `json:"unalerted"` // failed runs since the previous alert that were not alerted
	OutputTail   string    `json:"output_tail,omitempty"`
	Log          string    `json:"log"`
}

// invalid rejects input as shell does, so every invalid input of the group is
// a *shell.InvalidRequestError.
func invalid(format string, a ...any) error {
	return &shell.InvalidRequestError{Reason: fmt.Sprintf(format, a...)}
}

// validName reports whether name can name a job's files: letters, digits,
// '.', '_' and '-', starting with a letter or digit.
func validName(name string) bool {
	if name == "" || len(name) > 100 || !isAlnum(rune(name[0])) {
		return false
	}
	return !strings.ContainsFunc(name, func(c rune) bool { return !isAlnum(c) && !strings.ContainsRune("._-", c) })
}

func isAlnum(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// Validate checks r without running anything.
func (r Request) Validate() error {
	switch {
	case !validName(r.Name):
		return invalid("job name %q must be letters, digits, '.', '_' or '-', start with a letter or digit, and be at most 100 bytes", r.Name)
	case len(r.Command) == 0 || r.Command[0] == "":
		return invalid("no command to run: give it after --")
	case r.Timeout <= 0:
		return invalid("timeout must be positive, got %s", r.Timeout)
	case r.OnAlert != "" && !filepath.IsAbs(r.OnAlert):
		return invalid("alert hook %q is not an absolute path", r.OnAlert)
	}
	for _, name := range r.Pass {
		if name == "" || strings.Contains(name, "=") {
			return invalid("%q is not an environment variable name", name)
		}
	}
	return nil
}

// jobDir is the group's directory. A root that cannot be found is the
// caller's invalid input, as the other groups report it (P5).
func jobDir(environ []string) (string, error) {
	dir, err := store.Dir(environ, "job")
	if errors.Is(err, store.ErrNoRoot) {
		return "", invalid("%v", err)
	}
	return dir, err
}

// LogPath is the log of job name in dir.
func LogPath(dir, name string) string { return filepath.Join(dir, name+".log") }

func statePath(dir, name string) string { return filepath.Join(dir, name+".json") }

// Run runs r once, unless the previous run of the job still holds its lock.
// A non-nil error means nothing was run.
func Run(ctx context.Context, r Request) (Record, error) {
	if err := r.Validate(); err != nil {
		return Record{}, err
	}
	dir, err := jobDir(r.Environ)
	if err != nil {
		return Record{}, err
	}
	release, err := store.TryLock(filepath.Join(dir, r.Name+".lock"), true)
	if errors.Is(err, store.ErrLocked) {
		now := time.Now().UTC()
		rec := Record{Name: r.Name, Status: StatusSkipped, StartedAt: now, FinishedAt: now, Alert: AlertNone,
			Message: "the previous run is still going"}
		if log, err := store.OpenAppend(LogPath(dir, r.Name)); err == nil {
			fmt.Fprintf(log, "=== %s  skipped: the previous run is still going ===\n", stamp(now))
			log.Close()
		}
		return rec, nil
	}
	if err != nil {
		return Record{}, err
	}
	defer release()
	logPath := LogPath(dir, r.Name)
	if err := store.Rotate(logPath, logMax, logKeep); err != nil {
		return Record{}, err
	}
	log, err := store.OpenAppend(logPath)
	if err != nil {
		return Record{}, err
	}
	defer log.Close()
	st, err := readState(dir, r.Name)
	if err != nil {
		fmt.Fprintf(log, "aits: the state of job %s could not be read, starting afresh: %v\n", r.Name, err)
		st = State{Name: r.Name}
	}

	env := shell.Environ(r.Environ, r.Pass)
	rec := Record{Name: r.Name, StartedAt: time.Now().UTC()}
	fmt.Fprintf(log, "=== %s  start ===\n", stamp(rec.StartedAt))
	offset, _ := log.Seek(0, io.SeekEnd)
	if name := unset(env, r.Pass); name != "" {
		// A scheduled job whose environment lacks a variable is a failure
		// to alert, not a usage error nobody sees.
		rec.Status, rec.Message = StatusStartFailed, "environment variable "+name+" is not set"
		fmt.Fprintf(log, "aits: %s\n", rec.Message)
	} else {
		rec.Status, rec.ExitCode, rec.Message = execute(ctx, r.Kill, r.Command, env, r.Timeout, nil, log)
	}
	rec.FinishedAt = time.Now().UTC()
	rec.DurationMS = rec.FinishedAt.Sub(rec.StartedAt).Milliseconds()
	tail := readTail(logPath, offset)
	end := fmt.Sprintf("=== %s  %s", stamp(rec.FinishedAt), rec.Status)
	if rec.ExitCode != nil {
		end += fmt.Sprintf("  exit=%d", *rec.ExitCode)
	}
	fmt.Fprintf(log, "%s  %s ===\n", end, (time.Duration(rec.DurationMS) * time.Millisecond).String())

	var n *Notice
	rec.Alert = AlertNone
	if rec.Status != StatusStopped { // stopped from outside (launchctl, shutdown): not the job's failure
		prev := st.Failing
		rec.Alert, st.Failing = decide(prev, rec.Status == StatusOK, signature(rec, tail), rec.FinishedAt)
		n = notice(rec, prev, st.Failing, tail, logPath)
	}
	st.Last = &rec
	if rec.Status == StatusOK {
		st.LastOKAt = rec.FinishedAt
	}
	if err := writeState(dir, st); err != nil {
		return Record{}, err
	}
	if n != nil && r.OnAlert != "" {
		rec.AlertError = deliver(r, env, *n, log)
	}
	return rec, nil
}

// execute runs argv with env in its own process group, its stdin /dev/null
// and its stdout and stderr out, until it exits, times out or ctx is done
// (P10). On a timeout or a stop the group gets SIGTERM, and SIGKILL killGrace
// later or at once once kill is closed; once argv exits, whatever it left in
// its group is killed. stdin, if not nil, is the command's stdin.
func execute(ctx context.Context, kill <-chan struct{}, argv, env []string, timeout time.Duration, stdin io.Reader, out *os.File) (Status, *int, string) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdin = stdin
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		msg := fmt.Sprintf("start %s: %v", argv[0], err)
		fmt.Fprintf(out, "aits: %s\n", msg)
		return StatusStartFailed, nil, msg
	}
	pgid := cmd.Process.Pid
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	deadline, stop := time.After(timeout), ctx.Done()
	var grace <-chan time.Time
	var armed <-chan struct{}
	var timedOut, stopped bool
	terminate := func() {
		if grace == nil {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
			grace, armed = time.After(killGrace), kill
		}
	}
	for running := true; running; {
		select {
		case <-done:
			running = false
		case <-deadline:
			deadline, timedOut = nil, true
			terminate()
		case <-stop:
			stop, stopped = nil, true
			terminate()
		case <-grace:
			grace, armed = nil, nil
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		case <-armed:
			grace, armed = nil, nil
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL) // whatever it left running in its group
	code := cmd.ProcessState.ExitCode()
	switch {
	case stopped:
		return StatusStopped, &code, "aits got a stop signal; the command was stopped"
	case timedOut:
		return StatusTimeout, &code, fmt.Sprintf("timed out after %s; the command was stopped", timeout)
	case code == 0:
		return StatusOK, &code, ""
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return StatusFailed, &code, fmt.Sprintf("killed by signal %s", ws.Signal())
	}
	return StatusFailed, &code, fmt.Sprintf("exited with code %d", code)
}

// unset returns the first name in pass that env does not set, or "".
func unset(env, pass []string) string {
	for _, name := range pass {
		if !slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, name+"=") }) {
			return name
		}
	}
	return ""
}

// decide returns the alert of a run that succeeded (ok) or failed with
// signature sig at now, and the failing state after it.
func decide(f *Failing, ok bool, sig string, now time.Time) (Alert, *Failing) {
	if ok {
		if f != nil {
			return AlertRecovery, nil
		}
		return AlertNone, nil
	}
	if f == nil {
		return AlertFailure, &Failing{Since: now, Failures: 1, Signature: sig, Alerts: 1, LastAlertAt: now}
	}
	g := *f
	g.Failures++
	if g.Signature != sig {
		g.Signature, g.Alerts, g.LastAlertAt, g.Unalerted = sig, 1, now, 0
		return AlertFailure, &g
	}
	wait := backoffMax
	if g.Alerts <= len(backoff) {
		wait = backoff[g.Alerts-1]
	}
	if now.Sub(g.LastAlertAt) < wait {
		g.Unalerted++
		return AlertSuppressed, &g
	}
	g.Alerts, g.LastAlertAt, g.Unalerted = g.Alerts+1, now, 0
	return AlertRepeat, &g
}

// notice is the hook's view of a run whose alert is decided, or nil when
// there is nothing to deliver. prev is the failing state before the run, cur
// after it.
func notice(rec Record, prev, cur *Failing, tail []byte, log string) *Notice {
	n := &Notice{Name: rec.Name, Kind: rec.Alert, Status: rec.Status, ExitCode: rec.ExitCode, Message: rec.Message,
		FinishedAt: rec.FinishedAt, OutputTail: strings.ToValidUTF8(string(tail), ""), Log: log}
	switch rec.Alert {
	case AlertRecovery:
		n.FailingSince, n.Failures = prev.Since, prev.Failures
	case AlertFailure, AlertRepeat:
		n.FailingSince, n.Failures = cur.Since, cur.Failures
		if prev != nil {
			n.Unalerted = prev.Unalerted
		}
	default:
		return nil
	}
	return n
}

// deliver runs the hook with n on stdin, its output in the job's log, and
// returns why it failed, or "".
func deliver(r Request, env []string, n Notice, log *os.File) string {
	b, err := json.Marshal(n)
	if err != nil {
		return err.Error()
	}
	fmt.Fprintf(log, "=== %s  alert %s ===\n", stamp(time.Now().UTC()), n.Kind)
	status, _, msg := execute(context.Background(), nil, []string{r.OnAlert}, env, hookTimeout, bytes.NewReader(append(b, '\n')), log)
	if status == StatusOK {
		return ""
	}
	msg = "alert hook: " + msg
	fmt.Fprintf(log, "aits: %s\n", msg)
	return msg
}

// signature tells two failures apart: the status, the exit code and the end
// of the output, with numbers and long hex strings blanked out and runs of
// white space joined, so that a timestamp or a pid does not make the same
// failure new.
func signature(rec Record, tail []byte) string {
	if len(tail) > signatureSize {
		tail = tail[len(tail)-signatureSize:]
	}
	code := "-"
	if rec.ExitCode != nil {
		code = fmt.Sprint(*rec.ExitCode)
	}
	h := fnv.New64a()
	fmt.Fprintf(h, "%s %s\n%s", rec.Status, code, normalize(string(tail)))
	if rec.Status == StatusStartFailed {
		fmt.Fprint(h, normalize(rec.Message))
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

func normalize(s string) string {
	var b strings.Builder
	space := false
	for i := 0; i < len(s); {
		c := s[i]
		if isHex(c) {
			j := i
			digit := false
			for j < len(s) && isHex(s[j]) {
				digit = digit || s[j] >= '0' && s[j] <= '9'
				j++
			}
			if digit || j-i >= 8 {
				b.WriteByte('#')
			} else {
				b.WriteString(s[i:j])
			}
			i, space = j, false
			continue
		}
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if !space {
				b.WriteByte(' ')
			}
			i, space = i+1, true
			continue
		}
		b.WriteByte(c)
		i, space = i+1, false
	}
	return strings.TrimSpace(b.String())
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

// readTail returns the last tailSize bytes written to path after offset.
func readTail(path string, offset int64) []byte {
	f, fi, err := store.OpenRegular(path, os.O_RDONLY, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	from := max(offset, fi.Size()-tailSize)
	if from >= fi.Size() {
		return nil
	}
	b := make([]byte, fi.Size()-from)
	n, _ := f.ReadAt(b, from)
	return b[:n]
}

func stamp(t time.Time) string { return t.Format(time.RFC3339) }

// readState reads the state of job name; a job that never ran has an empty
// one.
func readState(dir, name string) (State, error) {
	f, _, err := store.OpenRegular(statePath(dir, name), os.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return State{Name: name}, nil
	}
	if err != nil {
		return State{}, err
	}
	defer f.Close()
	var st State
	if err := json.NewDecoder(f).Decode(&st); err != nil {
		return State{}, err
	}
	if st.Name != name {
		return State{}, fmt.Errorf("its name is %q, not %q", st.Name, name)
	}
	return st, nil
}

func writeState(dir string, st State) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return store.WriteFile(statePath(dir, st.Name), append(b, '\n'))
}

// List returns the state of every job that has run, by name, and the names
// whose state could not be read.
func List(environ []string) ([]State, []string, error) {
	dir, err := jobDir(environ)
	if err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	states, skipped := []State{}, []string{}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || strings.HasPrefix(name, ".") {
			continue
		}
		st, err := readState(dir, name)
		if err != nil || !validName(name) || !utf8.ValidString(name) {
			skipped = append(skipped, name)
			continue
		}
		states = append(states, st)
	}
	slices.SortFunc(states, func(a, b State) int { return strings.Compare(a.Name, b.Name) })
	return states, skipped, nil
}
