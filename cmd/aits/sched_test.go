package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

type heartbeat struct {
	File      string    `json:"file"`
	CheckedAt time.Time `json:"checked_at"`
	Next      []struct {
		Name string    `json:"name"`
		At   time.Time `json:"at"`
	} `json:"next"`
	Error string `json:"error"`
}

// sched is one AITS_HOME and jobs file for job sched runs.
type sched struct {
	home, spec string
}

func newSched(t *testing.T, spec string) sched {
	t.Helper()
	s := sched{home: t.TempDir(), spec: filepath.Join(t.TempDir(), "jobs.json")}
	if err := os.WriteFile(s.spec, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	return s
}

func schedID(file string) string {
	h := fnv.New64a()
	h.Write([]byte(file))
	return fmt.Sprintf("%016x", h.Sum64())
}

// lastChecked writes the heartbeat a sched that last checked at t would have
// left, so the next sched catches up from t.
func (s sched) lastChecked(t *testing.T, at time.Time) {
	t.Helper()
	dir := filepath.Join(s.home, "job")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"file": s.spec, "checked_at": at, "next": []any{}})
	if err := os.WriteFile(filepath.Join(dir, ".sched-"+schedID(s.spec)+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// start starts "aits job sched SPEC --json".
func (s sched) start(t *testing.T) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	cmd, stdout, stderr := call{env: []string{"AITS_HOME=" + s.home}, args: []string{"job", "sched", s.spec, "--json"}}.command(t)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd, stdout, stderr
}

// stop sends SIGTERM and returns how sched exited.
func stop(t *testing.T, cmd *exec.Cmd, stdout, stderr *bytes.Buffer) output {
	t.Helper()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return finish(t, err, stdout, stderr)
	case <-time.After(15 * time.Second):
		t.Fatal("job sched did not exit after SIGTERM")
	}
	return output{}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); !cond(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (s sched) list(t *testing.T) (jobList, []heartbeat) {
	t.Helper()
	out := call{env: []string{"AITS_HOME=" + s.home}, args: []string{"job", "list", "--json"}}.run(t)
	l := decodeOne[jobList](t, out.stdout)
	return l, l.Sched
}

// TestJobSchedCatchesUp: a job due many times while no sched ran runs once,
// a job for another host or never due does not run, and job list shows the
// heartbeat.
func TestJobSchedCatchesUp(t *testing.T) {
	s := newSched(t, fmt.Sprintf(`{"path": "/usr/bin:/bin", "jobs": [
	  {"name": "tick", "schedule": "* * * * *", "command": ["/bin/sh", "-c", "echo PATH=$PATH"]},
	  {"name": "never", "schedule": "0 0 30 2 *", "command": ["/bin/echo"]},
	  {"name": "elsewhere", "schedule": "* * * * *", "hosts": ["not-%s"], "command": ["/bin/echo"]}]}`, shortHost(t)))
	s.lastChecked(t, time.Now().Add(-time.Hour))
	cmd, stdout, stderr := s.start(t)
	waitUntil(t, "a run", func() bool { return strings.Contains(stdout.String(), "\n") })
	time.Sleep(500 * time.Millisecond) // room for a wrong second run
	out := stop(t, cmd, stdout, stderr)
	if out.code != 0 {
		t.Fatalf("exit %d: %s", out.code, out.stderr)
	}
	lines := strings.Split(strings.TrimSpace(out.stdout), "\n")
	if len(lines) != 1 {
		t.Fatalf("%d runs, want 1:\n%s", len(lines), out.stdout)
	}
	if rec := decodeOne[jobRecord](t, lines[0]); rec.Name != "tick" || rec.Status != "ok" {
		t.Fatalf("run %+v", rec)
	}
	j := jobs{home: s.home}
	if log := j.log(t, "tick"); strings.Count(log, " start ===") != 1 || !strings.Contains(log, "PATH=/usr/bin:/bin\n") {
		t.Errorf("log:\n%s", log)
	}

	l, hbs := s.list(t)
	if len(l.Jobs) != 1 || l.Jobs[0].Name != "tick" {
		t.Errorf("job list: %+v", l.Jobs)
	}
	if len(hbs) != 1 || hbs[0].File != s.spec || time.Since(hbs[0].CheckedAt) > 2*time.Minute {
		t.Fatalf("heartbeats %+v", hbs)
	}
	var names []string
	for _, n := range hbs[0].Next {
		names = append(names, n.Name)
		if n.Name == "tick" && !n.At.After(hbs[0].CheckedAt) || n.Name == "never" && !n.At.IsZero() {
			t.Errorf("next %+v", n)
		}
	}
	if !slices.Equal(names, []string{"tick", "never"}) {
		t.Errorf("next names %q", names)
	}
}

// TestJobSchedStopsRunningJobs pins P10: SIGTERM stops a running job's group
// and sched still records it, then exits 0.
func TestJobSchedStopsRunningJobs(t *testing.T) {
	s := newSched(t, `{"jobs": [{"name": "slow", "schedule": "* * * * *", "command": ["/bin/sh", "-c", "echo started; exec /bin/sleep 60"]}]}`)
	s.lastChecked(t, time.Now().Add(-time.Hour))
	cmd, stdout, stderr := s.start(t)
	j := jobs{home: s.home}
	waitUntil(t, "the job to start", func() bool {
		b, _ := os.ReadFile(filepath.Join(s.home, "job", "slow.log"))
		return strings.Contains(string(b), "started")
	})
	out := stop(t, cmd, stdout, stderr)
	if out.code != 0 {
		t.Fatalf("exit %d: %s", out.code, out.stderr)
	}
	if rec := decodeOne[jobRecord](t, out.stdout); rec.Status != "stopped" {
		t.Fatalf("run %+v", rec)
	}
	if !strings.Contains(j.log(t, "slow"), "stopped") {
		t.Errorf("log:\n%s", j.log(t, "slow"))
	}
}

func TestJobSchedRefuses(t *testing.T) {
	s := newSched(t, `{"jobs": [{"name": "a", "schedule": "61 * * * *", "command": ["/bin/echo"]}]}`)
	out := call{env: []string{"AITS_HOME=" + s.home}, args: []string{"job", "sched", s.spec, "--json"}}.run(t)
	if out.code != 2 || !strings.Contains(out.stdout, "invalid_input") || !strings.Contains(out.stdout, "minute") {
		t.Fatalf("exit %d: %s", out.code, out.stdout)
	}

	s = newSched(t, `{"jobs": [{"name": "a", "schedule": "0 0 30 2 *", "command": ["/bin/echo"]}]}`)
	cmd, stdout, stderr := s.start(t)
	waitUntil(t, "the heartbeat", func() bool {
		_, err := os.Stat(filepath.Join(s.home, "job", ".sched-"+schedID(s.spec)+".json"))
		return err == nil
	})
	out = call{env: []string{"AITS_HOME=" + s.home}, args: []string{"job", "sched", s.spec, "--json"}}.run(t)
	if out.code != 1 || !strings.Contains(out.stdout, "another job sched") {
		t.Fatalf("second sched: exit %d: %s", out.code, out.stdout)
	}
	if out := stop(t, cmd, stdout, stderr); out.code != 0 || out.stdout != "" {
		t.Fatalf("exit %d: %q %q", out.code, out.stdout, out.stderr)
	}
}

// launchd is a fake launchctl next to a fake crontab: it logs each call, and
// print succeeds while the agent is bootstrapped.
func (c cron) launchd(t *testing.T) {
	t.Helper()
	script := `#!/bin/sh
d=${0%/*}
echo "$*" >> "$d/launchctl.log"
case $1 in
print) [ -f "$d/loaded" ];;
bootstrap) : > "$d/loaded";;
bootout) /bin/rm -f "$d/loaded";;
*) exit 2;;
esac
`
	if err := os.WriteFile(filepath.Join(c.bin, "launchctl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func (c cron) launchctl(t *testing.T) []string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(c.bin, "launchctl.log"))
	_ = os.Remove(filepath.Join(c.bin, "launchctl.log"))
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

type schedApplied struct {
	Label  string `json:"label"`
	Plist  string `json:"plist"`
	Change string `json:"change"`
}

func TestJobApplySched(t *testing.T) {
	spec := `{"jobs": [{"name": "a", "schedule": "0 * * * *", "command": ["/bin/echo"]}]}`
	if runtime.GOOS != "darwin" {
		c := newCron(t, "")
		res, out := c.apply(t, spec, "--sched")
		if out.code != 2 || res.Error == nil || !strings.Contains(res.Error.Message, "only on macOS") {
			t.Fatalf("exit %d: %s", out.code, out.stdout)
		}
		return
	}
	c := newCron(t, "0 1 * * * /usr/bin/true\n")
	c.launchd(t)
	if _, out := c.apply(t, spec); out.code != 0 {
		t.Fatalf("cron apply: exit %d: %s", out.code, out.stdout)
	}
	c.launchctl(t)

	applySched := func(flags ...string) (applyResult, output) { return c.apply(t, spec, flags...) }
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	label := "aits.sched." + schedID(c.spec)
	plist := filepath.Join(c.home, "Library", "LaunchAgents", label+".plist")

	res, out := applySched("--sched", "--dry-run")
	if out.code != 0 || res.Sched == nil || res.Sched.Change != "added" || !slices.Equal(res.Removed, []string{"a"}) {
		t.Fatalf("dry run: exit %d: %s", out.code, out.stdout)
	}
	if _, err := os.Stat(plist); err == nil || !strings.Contains(c.table(t), "job run a") {
		t.Fatal("a dry run changed something")
	}

	res, out = applySched("--sched")
	if out.code != 0 || *res.Sched != (schedApplied{label, plist, "added"}) || !slices.Equal(res.Removed, []string{"a"}) {
		t.Fatalf("exit %d: %s", out.code, out.stdout)
	}
	if got := c.table(t); got != "0 1 * * * /usr/bin/true\n" {
		t.Errorf("crontab still has the block:\n%s", got)
	}
	b, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<string>" + label + "</string>", "<string>" + aitsBin + "</string>", "<string>sched</string>", "<string>" + c.spec + "</string>", "<key>KeepAlive</key>", "<string>" + c.aitsHome + "</string>"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("plist lacks %s:\n%s", want, b)
		}
	}
	if got := c.launchctl(t); !slices.Contains(got, "bootstrap "+domain+" "+plist) {
		t.Errorf("launchctl calls %q", got)
	}

	res, out = applySched("--sched")
	if out.code != 0 || res.Sched.Change != "unchanged" || len(res.Removed) != 0 {
		t.Fatalf("again: exit %d: %s", out.code, out.stdout)
	}
	if got := c.launchctl(t); slices.ContainsFunc(got, func(s string) bool { return !strings.HasPrefix(s, "print ") }) {
		t.Errorf("an unchanged apply ran %q", got)
	}

	res, out = applySched()
	if out.code != 0 || res.Sched.Change != "removed" || !slices.Equal(res.Added, []string{"a"}) {
		t.Fatalf("back to cron: exit %d: %s", out.code, out.stdout)
	}
	if _, err := os.Stat(plist); err == nil || !strings.Contains(c.table(t), "job run a") {
		t.Errorf("plist kept or block missing:\n%s", c.table(t))
	}
	if got := c.launchctl(t); !slices.Contains(got, "bootout "+domain+"/"+label) {
		t.Errorf("launchctl calls %q", got)
	}
	if res, _ := applySched(); res.Sched != nil {
		t.Errorf("sched %+v after the agent was removed", res.Sched)
	}

	if err := os.MkdirAll(filepath.Dir(plist), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, []byte("<plist/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, out = applySched("--sched")
	if out.code != 2 || res.Error == nil || !strings.Contains(res.Error.Message, "not written by job apply") {
		t.Fatalf("foreign plist: exit %d: %s", out.code, out.stdout)
	}
	if b, _ := os.ReadFile(plist); string(b) != "<plist/>" || !strings.Contains(c.table(t), "job run a") {
		t.Error("a refused apply changed something")
	}
}
