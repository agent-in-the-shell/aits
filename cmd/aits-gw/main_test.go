package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var gwBin string // the aits-gw binary under test (P12)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "aits-gw-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	gwBin = filepath.Join(dir, "aits-gw")
	build := exec.Command("go", "build", "-o", gwBin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build aits-gw: %v\n%s", err, b)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	// The first run of a fresh executable can be slow (macOS assesses it); a
	// test that signals serve must not race that against main's handler.
	_ = exec.Command(gwBin, "--help").Run()
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// home is one temporary HOME and AITS_HOME that several aits-gw runs share,
// as a put, a serve and a status on one machine do.
type home struct {
	t    *testing.T
	aits string // AITS_HOME
	env  []string
}

func newHome(t *testing.T) *home {
	h := &home{t: t, aits: t.TempDir()}
	h.env = []string{"PATH=" + t.TempDir(), "HOME=" + t.TempDir(), "AITS_HOME=" + h.aits}
	return h
}

type output struct {
	stdout, stderr string
	code           int
}

func (h *home) command(args ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	cmd := exec.Command(gwBin, args...)
	cmd.Env = h.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	return cmd, &stdout, &stderr
}

// run runs aits-gw to completion.
func (h *home) run(args ...string) output {
	h.t.Helper()
	cmd, stdout, stderr := h.command(args...)
	return finish(h.t, cmd.Run(), stdout, stderr)
}

func finish(t *testing.T, err error, stdout, stderr *bytes.Buffer) output {
	t.Helper()
	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		t.Fatalf("run aits-gw: %v", err)
	}
	return output{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// server is a running "aits-gw serve --json".
type server struct {
	cmd            *exec.Cmd
	stdout, stderr *bytes.Buffer
	done           chan error
}

// serve starts "aits-gw serve --json" with extra args.
func (h *home) serve(extra ...string) *server {
	h.t.Helper()
	cmd, stdout, stderr := h.command(append([]string{"serve", "--json"}, extra...)...)
	if err := cmd.Start(); err != nil {
		h.t.Fatal(err)
	}
	s := &server{cmd: cmd, stdout: stdout, stderr: stderr, done: make(chan error, 1)}
	go func() { s.done <- cmd.Wait() }()
	h.t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			<-s.done
		}
	})
	h.until("serve holds the lock", func(st status) bool { return st.Serving })
	return s
}

// stop sends SIGTERM and returns serve's result.
func (s *server) stop(t *testing.T) output {
	t.Helper()
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-s.done:
		return finish(t, err, s.stdout, s.stderr)
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop within 10s of SIGTERM")
	}
	return output{}
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type status struct {
	Status         string `json:"status"`
	Serving        bool   `json:"serving"`
	Spooled        int    `json:"spooled"`
	Rejected       int    `json:"rejected"`
	Events         int    `json:"events"`
	Duplicates     int    `json:"duplicates"`
	LastIngestedAt string `json:"last_ingested_at"`
	SchemaVersion  int    `json:"schema_version"`
}

type putResult struct {
	Status   string `json:"status"`
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	SourceID string `json:"source_id"`
}

// wantError checks that out is a failure with exit code exit and, as --json
// asks, only {"error":{"code":code,…}} on stdout and nothing on stderr (P5).
func wantError(t *testing.T, what string, out output, exit int, code string) {
	t.Helper()
	var r struct {
		Error *errorBody `json:"error"`
	}
	decode(t, out.stdout, &r)
	if out.code != exit || r.Error == nil || r.Error.Code != code || r.Error.Message == "" || out.stderr != "" {
		t.Errorf("%s: code=%d stdout=%q stderr=%q, want exit %d %s", what, out.code, out.stdout, out.stderr, exit, code)
	}
}

// decode parses stdout as exactly one JSON value into v.
func decode(t *testing.T, stdout string, v any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("stdout is not one JSON result: %v: %q", err, stdout)
	}
	if dec.More() {
		t.Fatalf("stdout holds more than one JSON value: %q", stdout)
	}
}

func (h *home) status() status {
	h.t.Helper()
	out := h.run("status", "--json")
	var st status
	decode(h.t, out.stdout, &st)
	if out.code != 0 || st.Status != "ok" || out.stderr != "" {
		h.t.Fatalf("status: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
	return st
}

// until polls status until ok holds, failing after 10s.
func (h *home) until(what string, ok func(status) bool) status {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := h.status()
		if ok(st) {
			return st
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("waited 10s for %s; last status %+v", what, st)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *home) put(args ...string) putResult {
	h.t.Helper()
	out := h.run(append([]string{"event", "put", "--json"}, args...)...)
	var r putResult
	decode(h.t, out.stdout, &r)
	if out.code != 0 || r.Status != "spooled" || r.ID == "" || out.stderr != "" {
		h.t.Fatalf("event put %q: code=%d stdout=%q stderr=%q", args, out.code, out.stdout, out.stderr)
	}
	return r
}

// TestAcceptance runs the scenario of #165: a put with no server, a serve
// that records it, a re-send counted as a duplicate, and a SIGTERM that
// stops serve with its result.
func TestAcceptance(t *testing.T) {
	h := newHome(t)
	r := h.put("report.ready", "--source-id", "2026-11-21-am", "path=/tmp/r.md")
	if r.Kind != "report.ready" || r.SourceID != "2026-11-21-am" {
		t.Errorf("put result %+v", r)
	}
	want := status{Status: "ok", Spooled: 1, SchemaVersion: 1}
	if st := h.status(); st != want {
		t.Errorf("status before serve = %+v, want %+v", st, want)
	}

	s := h.serve()
	h.until("the first event to be recorded", func(st status) bool { return st.Spooled == 0 && st.Events == 1 })
	h.put("report.ready", "--source-id", "2026-11-21-am", "path=/tmp/r.md")
	st := h.until("the re-send to be counted", func(st status) bool { return st.Duplicates == 1 && st.Spooled == 0 })
	if !st.Serving || st.Spooled != 0 || st.Events != 1 || st.LastIngestedAt == "" {
		t.Errorf("status while serving = %+v", st)
	}
	if _, err := time.Parse(time.RFC3339Nano, st.LastIngestedAt); err != nil {
		t.Errorf("last_ingested_at %q: %v", st.LastIngestedAt, err)
	}

	out := s.stop(t)
	var res struct {
		Status   string `json:"status"`
		Ingested int    `json:"ingested"`
	}
	decode(t, out.stdout, &res)
	if out.code != 0 || res.Status != "stopped" || res.Ingested != 2 {
		t.Errorf("serve after SIGTERM: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
	if st := h.status(); st.Serving {
		t.Errorf("status after serve stopped still says serving: %+v", st)
	}
}

// TestPutText checks the result without --json: the event id alone.
func TestPutText(t *testing.T) {
	h := newHome(t)
	out := h.run("event", "put", "job.done", "--source-id", "a")
	if id := strings.TrimSpace(out.stdout); out.code != 0 || id == "" || strings.ContainsAny(id, " \n{") || out.stderr != "" {
		t.Fatalf("put: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
	out = h.run("status")
	for _, line := range []string{"serving: false", "spooled: 1", "rejected: 0", "events: 0", "duplicates: 0", "last_ingested_at: ", "schema_version: 1"} {
		if !strings.Contains(out.stdout, line+"\n") {
			t.Errorf("status text lacks %q: %q", line, out.stdout)
		}
	}
}

// TestPutInvalid checks the failure cases of #165 for put: each is exit 2,
// invalid_input, as JSON on stdout with --json (P5, P6), and nothing is
// spooled.
func TestPutInvalid(t *testing.T) {
	for name, args := range map[string][]string{
		"no kind":            {"--source-id", "a"},
		"uppercase kind":     {"Report", "--source-id", "a"},
		"kind starts with .": {".report", "--source-id", "a"},
		"kind with space":    {"report ready", "--source-id", "a"},
		"long kind":          {strings.Repeat("k", 129), "--source-id", "a"},
		"no source id":       {"report"},
		"empty source id":    {"report", "--source-id="},
		"control char id":    {"report", "--source-id", "a\nb"},
		"not key=value":      {"report", "--source-id", "a", "path"},
		"empty key":          {"report", "--source-id", "a", "=v"},
		"invalid key":        {"report", "--source-id", "a", "Path=v"},
		"repeated key":       {"report", "--source-id", "a", "k=1", "k=2"},
		"too large":          {"report", "--source-id", "a", "k=" + strings.Repeat("v", 64*1024)},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHome(t)
			wantError(t, "event put", h.run(append([]string{"event", "put", "--json"}, args...)...), 2, "invalid_input")
			if st := h.status(); st.Spooled != 0 {
				t.Errorf("an invalid put was spooled: %+v", st)
			}
		})
	}
}

// TestErrorsWithoutJSON checks P5's other channel: without --json the error
// goes to stderr, named after aits-gw, and stdout stays empty.
func TestErrorsWithoutJSON(t *testing.T) {
	out := newHome(t).run("event", "put", "report")
	if out.code != 2 || out.stdout != "" || !strings.HasPrefix(out.stderr, "aits-gw: ") || !strings.Contains(out.stderr, "--source-id") {
		t.Errorf("code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
}

func TestNoArguments(t *testing.T) {
	for _, cmd := range []string{"serve", "status"} {
		wantError(t, cmd+" extra", newHome(t).run(cmd, "extra", "--json"), 2, "invalid_input")
	}
}

// TestSecondServe checks that only one serve runs: a second exits 1 with
// already_serving and leaves the first serving.
func TestSecondServe(t *testing.T) {
	h := newHome(t)
	s := h.serve()
	wantError(t, "second serve", h.run("serve", "--json"), 1, "already_serving")
	if !h.status().Serving {
		t.Error("the first serve stopped serving")
	}
	if out := s.stop(t); out.code != 0 {
		t.Errorf("first serve: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
}

// TestRejected checks that a spool file that is not an event is moved to
// spool/rejected and reported on stderr, and that serve keeps going.
func TestRejected(t *testing.T) {
	h := newHome(t)
	h.status() // creates the directories
	spool := filepath.Join(h.aits, "gw", "spool")
	for name, body := range map[string]string{
		"00-not-json.json": "{not json",
		"01-bad-kind.json": `{"id":"x","kind":"BAD","source_id":"a","payload":{},"put_at":"t"}`,
	} {
		if err := os.WriteFile(filepath.Join(spool, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := h.serve()
	h.until("both files to be rejected", func(st status) bool { return st.Rejected == 2 && st.Spooled == 0 })
	h.put("job.done", "--source-id", "after")
	h.until("serve to keep recording", func(st status) bool { return st.Events == 1 })
	out := s.stop(t)
	for _, name := range []string{"00-not-json.json", "01-bad-kind.json"} {
		if _, err := os.Stat(filepath.Join(spool, "rejected", name)); err != nil {
			t.Errorf("rejected file: %v", err)
		}
		if _, err := os.Stat(filepath.Join(spool, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still in the spool: %v", name, err)
		}
		if !strings.Contains(out.stderr, "rejected spool file "+name) {
			t.Errorf("stderr does not report %s: %q", name, out.stderr)
		}
	}
	if !strings.Contains(out.stdout, `"ingested":1`) || out.code != 0 {
		t.Errorf("serve: code=%d stdout=%q", out.code, out.stdout)
	}
}

// TestPermissions checks P7 on disk: directories 0700, files 0600, also when
// they existed with looser modes.
func TestPermissions(t *testing.T) {
	h := newHome(t)
	gw := filepath.Join(h.aits, "gw")
	if err := os.MkdirAll(filepath.Join(gw, "spool"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.put("job.done", "--source-id", "a")
	s := h.serve()
	h.until("the event to be recorded", func(st status) bool { return st.Events == 1 })
	h.put("job.done", "--source-id", "b")
	s.stop(t)
	err := filepath.WalkDir(gw, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		want := fs.FileMode(0o600)
		if d.IsDir() {
			want = 0o700
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s has mode %o, want %o", path, got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"gw.db", "serve.lock", "spool", "spool/rejected"} {
		if _, err := os.Stat(filepath.Join(gw, p)); err != nil {
			t.Errorf("expected %s: %v", p, err)
		}
	}
}

// TestSchemaTooNew checks that a database written by a newer aits-gw is
// refused rather than read or migrated down.
func TestSchemaTooNew(t *testing.T) {
	h := newHome(t)
	h.status()
	db, err := sql.Open("sqlite", filepath.Join(h.aits, "gw", "gw.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for _, cmd := range []string{"status", "serve"} {
		wantError(t, cmd, h.run(cmd, "--json"), 1, "schema_too_new")
	}
}
