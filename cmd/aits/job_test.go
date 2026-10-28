package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

type jobRecord struct {
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	ExitCode   *int       `json:"exit_code"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt time.Time  `json:"finished_at"`
	DurationMS int64      `json:"duration_ms"`
	Message    string     `json:"message"`
	Alert      string     `json:"alert"`
	AlertError string     `json:"alert_error"`
	Error      *errorBody `json:"error"`
}

type jobNotice struct {
	Name         string    `json:"name"`
	Kind         string    `json:"kind"`
	Status       string    `json:"status"`
	ExitCode     *int      `json:"exit_code"`
	Message      string    `json:"message"`
	FinishedAt   time.Time `json:"finished_at"`
	FailingSince time.Time `json:"failing_since"`
	Failures     int       `json:"failures"`
	Unalerted    int       `json:"unalerted"`
	OutputTail   string    `json:"output_tail"`
	Log          string    `json:"log"`
}

// jobs is one AITS_HOME and an alert hook that appends what it gets to a
// file, for a sequence of job runs.
type jobs struct {
	home, hook, alerts string
}

func newJobs(t *testing.T) jobs {
	t.Helper()
	dir := t.TempDir()
	j := jobs{home: t.TempDir(), hook: filepath.Join(dir, "hook"), alerts: filepath.Join(dir, "alerts.jsonl")}
	script := fmt.Sprintf("#!/bin/sh\n/bin/cat >> %q\n", j.alerts)
	if err := os.WriteFile(j.hook, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return j
}

func (j jobs) call(args ...string) call {
	return call{env: []string{"AITS_HOME=" + j.home}, args: append([]string{"job"}, args...)}
}

// run runs "aits job run NAME --json --on-alert HOOK [flags] -- /bin/sh -c SCRIPT".
func (j jobs) run(t *testing.T, name, script string, flags ...string) (jobRecord, output) {
	t.Helper()
	args := append([]string{"run", name, "--json", "--on-alert", j.hook}, flags...)
	out := j.call(append(args, "--", "/bin/sh", "-c", script)...).run(t)
	return decodeOne[jobRecord](t, out.stdout), out
}

// notices returns the alerts the hook got since the last call.
func (j jobs) notices(t *testing.T) []jobNotice {
	t.Helper()
	f, err := os.Open(j.alerts)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var ns []jobNotice
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		ns = append(ns, decodeOne[jobNotice](t, sc.Text()))
	}
	if err := os.Remove(j.alerts); err != nil {
		t.Fatal(err)
	}
	return ns
}

func (j jobs) log(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(j.home, "job", name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestJobRunOK(t *testing.T) {
	j := newJobs(t)
	rec, out := j.run(t, "backup", "echo hello; echo oops >&2")
	if out.code != 0 || rec.Status != "ok" || rec.ExitCode == nil || *rec.ExitCode != 0 || rec.Alert != "none" || rec.Message != "" {
		t.Fatalf("exit %d, record %+v, stderr %q", out.code, rec, out.stderr)
	}
	if rec.StartedAt.IsZero() || rec.FinishedAt.Before(rec.StartedAt) {
		t.Errorf("times %s, %s", rec.StartedAt, rec.FinishedAt)
	}
	log := j.log(t, "backup")
	for _, want := range []string{" start ===\nhello\noops\n=== ", "  ok  exit=0  "} {
		if !strings.Contains(log, want) {
			t.Errorf("log %q lacks %q", log, want)
		}
	}
	if ns := j.notices(t); len(ns) != 0 {
		t.Errorf("a success after nothing alerted: %+v", ns)
	}
	text := j.call("run", "backup", "--", "/bin/sh", "-c", "true").run(t)
	if text.code != 0 || text.stdout != "backup  ok  none\n" || text.stderr != "" {
		t.Errorf("text output: exit %d, stdout %q, stderr %q", text.code, text.stdout, text.stderr)
	}
}

// TestJobRunAlerts follows one job through a failure, a repeat within its
// backoff, a different failure and a recovery.
func TestJobRunAlerts(t *testing.T) {
	j := newJobs(t)
	rec, out := j.run(t, "flaky", "echo disk full at 12:00:01 >&2; exit 3")
	if out.code != 1 || rec.Status != "failed" || *rec.ExitCode != 3 || rec.Message != "exited with code 3" || rec.Alert != "failure" {
		t.Fatalf("exit %d, record %+v", out.code, rec)
	}
	ns := j.notices(t)
	if len(ns) != 1 || ns[0].Kind != "failure" || ns[0].Name != "flaky" || ns[0].Failures != 1 ||
		ns[0].OutputTail != "disk full at 12:00:01\n" || ns[0].Log != filepath.Join(j.home, "job", "flaky.log") ||
		!ns[0].FailingSince.Equal(rec.FinishedAt) {
		t.Fatalf("notices %+v", ns)
	}
	// The same failure, another time stamp: within the backoff.
	if rec, _ = j.run(t, "flaky", "echo disk full at 12:05:09 >&2; exit 3"); rec.Alert != "suppressed" {
		t.Errorf("repeat: alert %s", rec.Alert)
	}
	if ns := j.notices(t); len(ns) != 0 {
		t.Errorf("a suppressed repeat was delivered: %+v", ns)
	}
	// Another exit code is another failure.
	if rec, _ = j.run(t, "flaky", "echo disk full at 12:10:00 >&2; exit 4"); rec.Alert != "failure" {
		t.Errorf("new failure: alert %s", rec.Alert)
	}
	if ns := j.notices(t); len(ns) != 1 || ns[0].Failures != 3 || ns[0].Unalerted != 1 {
		t.Errorf("new failure notices %+v", ns)
	}
	if rec, _ = j.run(t, "flaky", "true"); rec.Alert != "recovery" {
		t.Errorf("recovery: alert %s", rec.Alert)
	}
	if ns := j.notices(t); len(ns) != 1 || ns[0].Kind != "recovery" || ns[0].Status != "ok" || ns[0].Failures != 3 {
		t.Errorf("recovery notices %+v", ns)
	}
	if rec, _ = j.run(t, "flaky", "true"); rec.Alert != "none" {
		t.Errorf("ok after recovery: alert %s", rec.Alert)
	}
	if rec, _ = j.run(t, "flaky", "exit 3"); rec.Alert != "failure" {
		t.Errorf("a fresh failure: alert %s", rec.Alert)
	}
}

func TestJobRunTimeoutStopsTheGroup(t *testing.T) {
	j := newJobs(t)
	pidfile := filepath.Join(t.TempDir(), "child.pid")
	start := time.Now()
	rec, out := j.run(t, "slow", fmt.Sprintf("/bin/sleep 300 & echo $! > %q; wait", pidfile), "--timeout", "1s")
	if out.code != 1 || rec.Status != "timeout" || rec.Alert != "failure" || !strings.Contains(rec.Message, "timed out after 1s") {
		t.Fatalf("exit %d, record %+v", out.code, rec)
	}
	if time.Since(start) > 15*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
	assertGone(t, readPID(t, pidfile))
}

func TestJobRunKillsLeftovers(t *testing.T) {
	j := newJobs(t)
	pidfile := filepath.Join(t.TempDir(), "child.pid")
	rec, _ := j.run(t, "leaky", fmt.Sprintf("/bin/sleep 300 >/dev/null 2>&1 & echo $! > %q", pidfile))
	if rec.Status != "ok" {
		t.Fatalf("record %+v", rec)
	}
	assertGone(t, readPID(t, pidfile))
}

// TestJobRunOverlapIsSkipped runs a job while its previous run is still
// going: the second does not run its command and exits 0.
func TestJobRunOverlapIsSkipped(t *testing.T) {
	j := newJobs(t)
	dir := t.TempDir()
	started, marker := filepath.Join(dir, "started"), filepath.Join(dir, "second-ran")
	first := j.call("run", "busy", "--json", "--", "/bin/sh", "-c", fmt.Sprintf("echo $$ > %q; exec /bin/sleep 300", started)).start(t)
	waitFile(t, started)
	rec, out := j.run(t, "busy", fmt.Sprintf("echo > %q", marker))
	if out.code != 0 || rec.Status != "skipped" || rec.Alert != "none" || rec.ExitCode != nil {
		t.Errorf("exit %d, record %+v", out.code, rec)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the overlapping run ran its command")
	}
	first.signal(t, syscall.SIGTERM)
	if rec := decodeOne[jobRecord](t, first.waitOrKill(t, 15*time.Second).stdout); rec.Status != "stopped" {
		t.Errorf("first run: %+v", rec)
	}
	if !strings.Contains(j.log(t, "busy"), "  skipped: the previous run is still going ===\n") {
		t.Errorf("log %q", j.log(t, "busy"))
	}
}

// TestJobRunStoppedDoesNotAlert: a stop from outside ends the group and is
// not the job's failure.
func TestJobRunStoppedDoesNotAlert(t *testing.T) {
	j := newJobs(t)
	pidfile := filepath.Join(t.TempDir(), "child.pid")
	s := j.call("run", "stopped", "--json", "--on-alert", j.hook, "--", "/bin/sh", "-c",
		fmt.Sprintf("/bin/sleep 300 & echo $! > %q; wait", pidfile)).start(t)
	waitFile(t, pidfile)
	s.signal(t, syscall.SIGTERM)
	out := s.waitOrKill(t, 15*time.Second)
	rec := decodeOne[jobRecord](t, out.stdout)
	if out.code != 1 || rec.Status != "stopped" || rec.Alert != "none" {
		t.Errorf("exit %d, record %+v", out.code, rec)
	}
	assertGone(t, readPID(t, pidfile))
	if ns := j.notices(t); len(ns) != 0 {
		t.Errorf("a stop alerted: %+v", ns)
	}
}

func TestJobRunStartFailed(t *testing.T) {
	j := newJobs(t)
	out := j.call("run", "missing", "--json", "--on-alert", j.hook, "--", "/no/such/program").run(t)
	rec := decodeOne[jobRecord](t, out.stdout)
	if out.code != 1 || rec.Status != "start_failed" || rec.ExitCode != nil || rec.Alert != "failure" || !strings.Contains(rec.Message, "/no/such/program") {
		t.Errorf("exit %d, record %+v", out.code, rec)
	}
	if ns := j.notices(t); len(ns) != 1 || ns[0].Status != "start_failed" {
		t.Errorf("notices %+v", ns)
	}
	rec, out = j.run(t, "unset", "true", "--env", "AITS_TEST_UNSET")
	if out.code != 1 || rec.Status != "start_failed" || rec.Message != "environment variable AITS_TEST_UNSET is not set" {
		t.Errorf("exit %d, record %+v", out.code, rec)
	}
}

// TestJobRunEnvironment: the command and the hook get the allowlist and
// --env, never the rest of aits's environment (P8).
func TestJobRunEnvironment(t *testing.T) {
	j := newJobs(t)
	dump := filepath.Join(t.TempDir(), "env")
	c := j.call("run", "env", "--json", "--on-alert", j.hook, "--env", "AITS_TEST_PASSED", "--",
		"/bin/sh", "-c", fmt.Sprintf("/usr/bin/env > %q; exit 1", dump))
	c.env = append(c.env, canary, "AITS_TEST_PASSED=passed-value")
	out := c.run(t)
	assertNoCanary(t, out)
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(b)), "\n")
	if !slices.Contains(got, "AITS_TEST_PASSED=passed-value") {
		t.Errorf("--env variable missing from %q", got)
	}
	for _, kv := range got {
		if strings.HasPrefix(kv, canaryName+"=") || strings.HasPrefix(kv, "AITS_HOME=") {
			t.Errorf("variable outside the allowlist reached the command: %s", kv)
		}
	}
	for _, n := range j.notices(t) {
		if strings.Contains(n.OutputTail+n.Message, canarySecret) {
			t.Errorf("the canary reached the alert: %+v", n)
		}
	}
	if strings.Contains(j.log(t, "env"), canarySecret) {
		t.Error("the canary is in the log")
	}
}

// TestJobRunHookFails: a failing hook is reported and never changes the
// run's outcome.
func TestJobRunHookFails(t *testing.T) {
	j := newJobs(t)
	if err := os.WriteFile(j.hook, []byte("#!/bin/sh\necho hook broke >&2\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	rec, out := j.run(t, "nohook", "exit 5")
	if out.code != 1 || rec.Status != "failed" || *rec.ExitCode != 5 || rec.Alert != "failure" || rec.AlertError != "alert hook: exited with code 7" {
		t.Errorf("exit %d, record %+v", out.code, rec)
	}
	if !strings.Contains(j.log(t, "nohook"), "hook broke\naits: alert hook: exited with code 7\n") {
		t.Errorf("log %q", j.log(t, "nohook"))
	}
}

func TestJobRunInvalid(t *testing.T) {
	j := newJobs(t)
	for _, args := range [][]string{
		{"run", "--json"},
		{"run", "--json", "x"},
		{"run", "--json", "bad name", "--", "/usr/bin/true"},
		{"run", "--json", ".hidden", "--", "/usr/bin/true"},
		{"run", "--json", "x", "--timeout", "0s", "--", "/usr/bin/true"},
		{"run", "--json", "x", "--on-alert", "relative/hook", "--", "/usr/bin/true"},
		{"run", "--json", "x", "--env", "A=B", "--", "/usr/bin/true"},
		{"list", "--json", "extra"},
	} {
		out := j.call(args...).run(t)
		rec := decodeOne[jobRecord](t, out.stdout)
		if out.code != 2 || rec.Error == nil || rec.Error.Code != "invalid_input" {
			t.Errorf("%q: exit %d, stdout %q", args, out.code, out.stdout)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(j.home, "job")); len(entries) != 0 {
		t.Errorf("invalid input wrote %v", entries)
	}
}

func TestJobRunLogRotates(t *testing.T) {
	j := newJobs(t)
	for range 5 {
		if rec, _ := j.run(t, "chatty", "/usr/bin/head -c 5300000 /dev/zero"); rec.Status != "ok" {
			t.Fatalf("record %+v", rec)
		}
	}
	var logs []string
	entries, err := os.ReadDir(filepath.Join(j.home, "job"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "chatty.log") {
			logs = append(logs, e.Name())
		}
	}
	if want := []string{"chatty.log", "chatty.log.1", "chatty.log.2", "chatty.log.3"}; !slices.Equal(logs, want) {
		t.Errorf("logs %v, want %v", logs, want)
	}
}

type jobList struct {
	Jobs []struct {
		Name     string          `json:"name"`
		Last     jobRecord       `json:"last"`
		LastOKAt time.Time       `json:"last_ok_at"`
		Failing  json.RawMessage `json:"failing"`
	} `json:"jobs"`
	Skipped []string `json:"skipped"`
}

func TestJobList(t *testing.T) {
	j := newJobs(t)
	j.run(t, "b-fails", "exit 1")
	j.run(t, "a-works", "true")
	if err := os.WriteFile(filepath.Join(j.home, "job", "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := j.call("list", "--json").run(t)
	got := decodeOne[jobList](t, out.stdout)
	if out.code != 0 || len(got.Jobs) != 2 || got.Jobs[0].Name != "a-works" || got.Jobs[1].Name != "b-fails" ||
		got.Jobs[0].Failing != nil || got.Jobs[1].Failing == nil || got.Jobs[0].LastOKAt.IsZero() ||
		got.Jobs[1].Last.Status != "failed" || !slices.Equal(got.Skipped, []string{"broken"}) {
		t.Errorf("exit %d, list %s", out.code, out.stdout)
	}
	text := j.call("list").run(t)
	lines := strings.Split(strings.TrimSpace(text.stdout), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "a-works  ok  ") || !strings.HasPrefix(lines[1], "b-fails  failed  ") ||
		!strings.HasSuffix(lines[1], "Z") || text.stderr != "aits: skipped job broken whose state could not be read\n" {
		t.Errorf("text list: stdout %q, stderr %q", text.stdout, text.stderr)
	}
}
