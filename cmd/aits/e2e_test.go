package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-in-the-shell/aits/internal/shell"
)

var (
	aitsBin string // the aits binary under test (P12)
	fakeBin string // the fake vendor executable
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "aits-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	aitsBin = filepath.Join(dir, "aits")
	fakeBin = filepath.Join(dir, "fakevendor")
	for pkg, out := range map[string]string{".": aitsBin, "../../internal/shell/testdata/fakevendor": fakeBin} {
		build := exec.Command("go", "build", "-o", out, pkg)
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := build.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n%s", pkg, err, b)
			os.RemoveAll(dir)
			os.Exit(1)
		}
	}
	// Run aits once: the first run of a freshly built executable can be slow
	// (macOS assesses it), and a test that signals aits must not race that
	// first start against main installing its handler.
	_ = exec.Command(aitsBin, "--help").Run()
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// agentNames are the agents every shared scenario runs against: all of them,
// so a new agent fails here until the fake vendor speaks its dialect.
var agentNames = shell.Agents()

// eachAgent runs f as a parallel subtest for every agent.
func eachAgent(t *testing.T, f func(t *testing.T, agent string)) {
	t.Helper()
	for _, agent := range agentNames {
		t.Run(agent, func(t *testing.T) {
			t.Parallel()
			f(t, agent)
		})
	}
}

// agentArgv is the argv each agent must be started with; the prompt is never
// in it (P8).
var agentArgv = map[string][]string{
	"claude": {"-p", "--output-format", "stream-json", "--verbose"},
	"codex":  {"exec", "--json", "-"},
}

// vendor installs the fake vendor as claude, set to scenario.
func vendor(t *testing.T, scenario string) string {
	t.Helper()
	return vendorAs(t, "claude", scenario)
}

// vendorAs installs the fake vendor under the name agent, set to scenario,
// in a fresh directory and returns the directory, for use as PATH. The fake
// speaks the dialect its executable name says.
func vendorAs(t *testing.T, agent, scenario string) string {
	t.Helper()
	dir := t.TempDir()
	// A hard link, not a symlink: os.Executable resolves symlinks, and the fake
	// must find its scenario file and its name in this directory.
	if err := os.Link(fakeBin, filepath.Join(dir, agent)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scenario"), []byte(scenario), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// call is one invocation of the aits binary, always with a temporary HOME and
// AITS_HOME (P12).
type call struct {
	path  string // PATH for aits; empty means an empty directory
	dir   string // aits's working directory; empty means the test's
	stdin string
	env   []string // extra KEY=VALUE entries
	args  []string
}

type output struct {
	stdout, stderr string
	code           int
}

func (c call) command(t *testing.T) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	path := c.path
	if path == "" {
		path = t.TempDir()
	}
	cmd := exec.Command(aitsBin, c.args...)
	cmd.Dir = c.dir
	cmd.Env = append([]string{"PATH=" + path, "HOME=" + t.TempDir(), "AITS_HOME=" + t.TempDir()}, c.env...)
	cmd.Stdin = strings.NewReader(c.stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	return cmd, &stdout, &stderr
}

func (c call) run(t *testing.T) output {
	t.Helper()
	cmd, stdout, stderr := c.command(t)
	return finish(t, cmd.Run(), stdout, stderr)
}

func finish(t *testing.T, err error, stdout, stderr *bytes.Buffer) output {
	t.Helper()
	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		t.Fatalf("run aits: %v", err)
	}
	return output{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func submitArgs(extra ...string) []string { return agentArgs("claude", extra...) }

func agentArgs(agent string, extra ...string) []string {
	return append([]string{"shell", "submit", "--agent", agent}, extra...)
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type submitResult struct {
	Status     string     `json:"status"`
	Agent      string     `json:"agent"`
	SessionID  string     `json:"session_id"`
	Answer     string     `json:"answer"`
	ExitCode   *int       `json:"exit_code"`
	DurationMS *int64     `json:"duration_ms"`
	StderrTail string     `json:"stderr_tail"`
	Message    string     `json:"message"`
	Error      *errorBody `json:"error"`
}

// decodeOne parses stdout as exactly one JSON object with known fields (P5).
func decodeOne[T any](t *testing.T, stdout string) T {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	dec.DisallowUnknownFields()
	var v T
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("stdout holds more than one JSON value: %q", stdout)
	}
	return v
}

func decode(t *testing.T, stdout string) submitResult {
	t.Helper()
	return decodeOne[submitResult](t, stdout)
}

type record struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
	Dir   string   `json:"dir"`
	Env   []string `json:"env"`
}

func readRecord(t *testing.T, dir string) record {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "record.json"))
	if err != nil {
		t.Fatalf("the fake agent did not run: %v", err)
	}
	var r record
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// ran reports whether the fake agent in dir was started.
func ran(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "record.json"))
	return err == nil
}

func TestSubmitSuccess(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		bin, work := vendorAs(t, agent, "ok"), t.TempDir()
		out := call{path: bin, args: agentArgs(agent, "--cwd", work, "--json", "Summarize this repo")}.run(t)
		if out.code != 0 {
			t.Fatalf("exit %d, stdout %q, stderr %q", out.code, out.stdout, out.stderr)
		}
		r := decode(t, out.stdout)
		if r.Status != "ok" || r.Agent != agent || r.SessionID != "sess-fake-1" || r.Answer != "fake answer" ||
			r.ExitCode == nil || *r.ExitCode != 0 || r.DurationMS == nil || r.Message != "" || r.Error != nil {
			t.Fatalf("result = %+v", r)
		}
		rec := readRecord(t, bin)
		if rec.Stdin != "Summarize this repo" {
			t.Errorf("agent stdin = %q", rec.Stdin)
		}
		if want := agentArgv[agent]; !slices.Equal(rec.Args, want) {
			t.Errorf("agent argv = %q, want %q (the prompt must not be in argv)", rec.Args, want)
		}
		wantDir, _ := filepath.EvalSymlinks(work)
		if gotDir, _ := filepath.EvalSymlinks(rec.Dir); gotDir != wantDir {
			t.Errorf("agent dir = %q, want %q", gotDir, wantDir)
		}
	})
}

func TestSubmitTextOutput(t *testing.T) {
	out := call{path: vendor(t, "ok"), args: submitArgs("hello")}.run(t)
	if out.code != 0 || out.stdout != "fake answer\n" || out.stderr != "session: sess-fake-1\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
}

func TestSubmitPromptFromStdin(t *testing.T) {
	bin := vendor(t, "ok")
	out := call{path: bin, stdin: "from stdin", args: submitArgs("--json", "-")}.run(t)
	if out.code != 0 {
		t.Fatalf("exit %d: %s", out.code, out.stdout)
	}
	if rec := readRecord(t, bin); rec.Stdin != "from stdin" {
		t.Errorf("agent stdin = %q", rec.Stdin)
	}
}

func TestSubmitLargePromptFromStdin(t *testing.T) {
	bin := vendor(t, "ok")
	prompt := strings.Repeat("p", 1<<20)
	out := call{path: bin, stdin: prompt, args: submitArgs("--json", "-")}.run(t)
	if out.code != 0 {
		t.Fatalf("exit %d: %s", out.code, out.stdout)
	}
	if rec := readRecord(t, bin); rec.Stdin != prompt {
		t.Errorf("agent got %d bytes of prompt, want %d", len(rec.Stdin), len(prompt))
	}
}

func TestSubmitRelativeCwd(t *testing.T) {
	bin, parent := vendor(t, "ok"), t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	out := call{path: bin, dir: parent, args: submitArgs("--cwd", "sub", "--json", "hi")}.run(t)
	if out.code != 0 {
		t.Fatalf("exit %d: %s", out.code, out.stdout)
	}
	wantDir, _ := filepath.EvalSymlinks(filepath.Join(parent, "sub"))
	if gotDir, _ := filepath.EvalSymlinks(readRecord(t, bin).Dir); gotDir != wantDir {
		t.Errorf("agent dir = %q, want %q", gotDir, wantDir)
	}
}

func TestSubmitAgentNotFound(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		out := call{args: agentArgs(agent, "--json", "hi")}.run(t)
		if r := decode(t, out.stdout); out.code != 1 || r.Error == nil || r.Error.Code != "agent_not_found" {
			t.Fatalf("code=%d stdout=%q", out.code, out.stdout)
		}
	})
}

// An agent CLI that is found but cannot be started is agent_start_failed,
// and nothing runs (P6, #60).
func TestSubmitAgentStartFailed(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, agent), []byte("not a program\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		out := call{path: bin, args: agentArgs(agent, "--json", "hi")}.run(t)
		if r := decode(t, out.stdout); out.code != 1 || r.Error == nil || r.Error.Code != "agent_start_failed" || r.Status != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
		}
	})
}

// An agent first found in a relative PATH entry is refused: it would resolve
// against aits's directory, not the agent's (P6).
func TestSubmitRelativePATHEntry(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		bin := vendorAs(t, agent, "ok")
		out := call{dir: bin, path: ".:" + bin, args: agentArgs(agent, "--json", "hi")}.run(t)
		if r := decode(t, out.stdout); out.code != 1 || r.Error == nil || r.Error.Code != "agent_not_found" {
			t.Fatalf("code=%d stdout=%q", out.code, out.stdout)
		}
	})
}

func TestSubmitInvalidInputStartsNothing(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code string
	}{
		{"unknown flag", submitArgs("--bogus", "hi"), "unknown_flag"},
		{"unknown agent", []string{"shell", "submit", "--agent", "nope", "hi"}, "invalid_input"},
		{"missing agent", []string{"shell", "submit", "hi"}, "invalid_input"},
		{"empty prompt", submitArgs("   "), "invalid_input"},
		{"prompt and dash", submitArgs("hi", "-"), "invalid_input"},
		{"no prompt", submitArgs(), "invalid_input"},
		{"missing cwd", submitArgs("--cwd", "/nonexistent/aits-test", "hi"), "invalid_input"},
		{"empty cwd", submitArgs("--cwd=", "hi"), "invalid_input"},
		{"empty cwd value", submitArgs("--cwd", "", "hi"), "invalid_input"},
		{"zero timeout", submitArgs("--timeout", "0s", "hi"), "invalid_input"},
		{"bad timeout", submitArgs("--timeout", "soon", "hi"), "invalid_input"},
		{"unset env", submitArgs("--env", "AITS_TEST_UNSET", "hi"), "invalid_input"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := vendor(t, "ok")
			out := call{path: bin, args: append(tt.args, "--json")}.run(t)
			r := decode(t, out.stdout)
			if out.code != 2 || r.Error == nil || r.Error.Code != tt.code {
				t.Errorf("code=%d stdout=%q, want exit 2 and error %s", out.code, out.stdout, tt.code)
			}
			if ran(bin) {
				t.Errorf("the agent was started")
			}
		})
	}
}

// canary is a secret in aits's environment that reaches neither the agent nor
// aits's own output (P8).
const canary = "AITS_TEST_CANARY=secret-canary"

func assertNoCanary(t *testing.T, out output) {
	t.Helper()
	if _, secret, _ := strings.Cut(canary, "="); strings.Contains(out.stdout+out.stderr, secret) {
		t.Errorf("the canary secret is in aits's output: stdout=%q stderr=%q", out.stdout, out.stderr)
	}
}

func TestSubmitEnvironment(t *testing.T) {
	own := map[string][]string{
		"claude": {"ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR"},
		"codex":  {"OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN", "CODEX_HOME"},
	}
	eachAgent(t, func(t *testing.T, agent string) {
		bin := vendorAs(t, agent, "ok")
		env := []string{canary, "AITS_TEST_PASSED=passed-value"}
		for _, names := range own {
			for _, n := range names {
				env = append(env, n+"=v-"+n)
			}
		}
		out := call{path: bin, env: env, args: agentArgs(agent, "--env", "AITS_TEST_PASSED", "--json", "hi")}.run(t)
		if out.code != 0 {
			t.Fatalf("exit %d: %s", out.code, out.stdout)
		}
		assertNoCanary(t, out)
		got := readRecord(t, bin).Env
		if !slices.Contains(got, "AITS_TEST_PASSED=passed-value") {
			t.Errorf("--env variable missing from %q", got)
		}
		for _, kv := range got {
			if strings.HasPrefix(kv, "AITS_TEST_CANARY=") || strings.HasPrefix(kv, "AITS_HOME=") {
				t.Errorf("variable outside the allowlist reached the agent: %s", kv)
			}
		}
		if !slices.ContainsFunc(got, func(kv string) bool { return strings.HasPrefix(kv, "HOME=") }) {
			t.Errorf("allowlisted HOME missing from %q", got)
		}
		for owner, names := range own {
			for _, n := range names {
				if has := slices.Contains(got, n+"=v-"+n); has != (owner == agent) {
					t.Errorf("%s passed to %s: %v, want %v", n, agent, has, owner == agent)
				}
			}
		}
	})
}

func TestSubmitAgentFailed(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		out := call{path: vendorAs(t, agent, "exit_nonzero"), env: []string{canary}, args: agentArgs(agent, "--json", "hi")}.run(t)
		assertNoCanary(t, out)
		r := decode(t, out.stdout)
		if out.code != 1 || r.Status != "agent_failed" || r.ExitCode == nil || *r.ExitCode != 3 ||
			r.SessionID != "sess-fake-1" || r.Answer != "" || r.Message != "the agent exited with code 3" {
			t.Fatalf("code=%d result=%+v", out.code, r)
		}
		if !strings.HasSuffix(r.StderrTail, "boom\n") || len(r.StderrTail) > 4096 {
			t.Errorf("stderr_tail: %d bytes ending %q", len(r.StderrTail), r.StderrTail[max(0, len(r.StderrTail)-10):])
		}
	})
}

func TestSubmitAgentReportsError(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		for scenario, exit := range map[string]int{"agent_error": 1, "agent_error_exit0": 0} {
			t.Run(scenario, func(t *testing.T) {
				out := call{path: vendorAs(t, agent, scenario), args: agentArgs(agent, "--json", "hi")}.run(t)
				r := decode(t, out.stdout)
				if out.code != 1 || r.Status != "agent_failed" || r.ExitCode == nil || *r.ExitCode != exit ||
					r.SessionID != "sess-fake-1" || r.Answer != "" || r.Message != "the agent reported an error: fake failure" {
					t.Fatalf("code=%d result=%+v", out.code, r)
				}
			})
		}
	})
}

func TestSubmitBadOutput(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		for scenario, session := range map[string]string{
			"not_json":     "sess-fake-1", // the session id arrives after the bad line
			"no_result":    "sess-fake-1",
			"empty_result": "sess-fake-1",
			"no_session":   "",
		} {
			t.Run(scenario, func(t *testing.T) {
				out := call{path: vendorAs(t, agent, scenario), args: agentArgs(agent, "--json", "hi")}.run(t)
				r := decode(t, out.stdout)
				if out.code != 1 || r.Status != "bad_agent_output" || r.SessionID != session || r.Answer != "" || r.Message == "" {
					t.Fatalf("code=%d result=%+v, want session_id %q", out.code, r, session)
				}
			})
		}
	})
}

// resumeArgv is the argv each agent must be started with to continue session
// sess-fake-1.
var resumeArgv = map[string][]string{
	"claude": {"-p", "--output-format", "stream-json", "--verbose", "--resume", "sess-fake-1"},
	"codex":  {"exec", "--json", "resume", "sess-fake-1", "-"},
}

func TestSubmitResume(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		bin := vendorAs(t, agent, "ok")
		out := call{path: bin, args: agentArgs(agent, "--session", "sess-fake-1", "--json", "next step")}.run(t)
		if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" || r.SessionID != "sess-fake-1" {
			t.Fatalf("code=%d result=%+v", out.code, r)
		}
		rec := readRecord(t, bin)
		if want := resumeArgv[agent]; !slices.Equal(rec.Args, want) {
			t.Errorf("agent argv = %q, want %q (the prompt must not be in argv)", rec.Args, want)
		}
		if rec.Stdin != "next step" {
			t.Errorf("agent stdin = %q", rec.Stdin)
		}
	})
}

// A session id goes into the agent's argv, so an empty one, or one that could
// be read as a flag, is refused before anything starts (P6).
func TestSubmitInvalidSession(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		for _, id := range []string{"", "--dangerously-skip-permissions"} {
			bin := vendorAs(t, agent, "ok")
			out := call{path: bin, args: agentArgs(agent, "--session="+id, "--json", "hi")}.run(t)
			if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "invalid_input" {
				t.Errorf("--session=%q: code=%d stdout=%q", id, out.code, out.stdout)
			}
			if ran(bin) {
				t.Errorf("--session=%q: the agent was started", id)
			}
		}
	})
}

func TestSubmitUnknownSession(t *testing.T) {
	reason := map[string]string{
		"claude": "No conversation found with session ID: sess-fake-1",
		"codex":  "no rollout found for thread id sess-fake-1",
	}
	eachAgent(t, func(t *testing.T, agent string) {
		out := call{path: vendorAs(t, agent, "unknown_session"), args: agentArgs(agent, "--session", "sess-fake-1", "--json", "hi")}.run(t)
		r := decode(t, out.stdout)
		if out.code != 1 || r.Status != "agent_failed" || r.ExitCode == nil || *r.ExitCode != 1 ||
			!strings.Contains(r.Message+"\n"+r.StderrTail, reason[agent]) {
			t.Fatalf("code=%d result=%+v", out.code, r)
		}
	})
}

// An explicitly empty --cwd is refused, never read as "the current
// directory" (P6), with the same message as session search.
func TestSubmitEmptyCwd(t *testing.T) {
	bin := vendor(t, "ok")
	out := call{path: bin, args: submitArgs("--cwd=", "--json", "hi")}.run(t)
	if r := decode(t, out.stdout); out.code != 2 || r.Error == nil ||
		r.Error.Message != `invalid value "" for flag -cwd: needs a directory` {
		t.Errorf("--json: code=%d stdout=%q", out.code, out.stdout)
	}
	out = call{path: bin, args: submitArgs("--cwd", "", "hi")}.run(t)
	if out.code != 2 || out.stdout != "" || !strings.HasPrefix(out.stderr, "aits: ") || strings.Count(out.stderr, "\n") != 1 {
		t.Errorf("text: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
	if ran(bin) {
		t.Error("the agent was started")
	}
}

// A prompt on stdin over 16 MiB is invalid input and starts nothing; exactly
// 16 MiB is taken (#7).
func TestSubmitPromptFromStdinBound(t *testing.T) {
	bin := vendor(t, "ok")
	out := call{path: bin, stdin: strings.Repeat("p", 16<<20+1), args: submitArgs("--json", "-")}.run(t)
	if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "invalid_input" || ran(bin) {
		t.Errorf("over the bound: code=%d stdout=%q ran=%v", out.code, out.stdout, ran(bin))
	}
	prompt := strings.Repeat("p", 16<<20)
	out = call{path: bin, stdin: prompt, args: submitArgs("--json", "-")}.run(t)
	if rec := readRecord(t, bin); out.code != 0 || len(rec.Stdin) != len(prompt) {
		t.Errorf("at the bound: code=%d, agent got %d bytes", out.code, len(rec.Stdin))
	}
}
