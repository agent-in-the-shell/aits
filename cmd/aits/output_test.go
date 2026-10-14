package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// outputCase is one row of TestOutputChannels. A task row (args start with
// "task") runs against its own AITS_HOME, rebuilt by setup before each of
// its two runs, and states its exact output; "{ID}", "{DIR}" and
// "{CREATED_AT}" stand for the values of that run.
type outputCase struct {
	name     string
	scenario string // "" means no agent on PATH
	args     []string
	code     int

	setup  func(q queue, dir string) map[string]string // nil: an empty queue
	errMsg string                                      // --json error message, when it is an error and exact
	result func(t *testing.T, stdout string, v map[string]string)
	stdout string // text mode, exact
	stderr string
}

// TestOutputChannels enforces P5 for every kind of outcome: with --json,
// stdout is exactly one JSON object (an error envelope or a result) and stderr
// is empty; without it, stdout carries only the result and stderr the
// diagnostics, with an error printed as "aits: MESSAGE".
func TestOutputChannels(t *testing.T) {
	tests := []outputCase{
		{name: "unknown flag", scenario: "ok", args: submitArgs("--bogus", "hi"), code: 2},
		{name: "unknown command", scenario: "ok", args: []string{"nope"}, code: 2},
		{name: "invalid input", scenario: "ok", args: []string{"shell", "submit", "hi"}, code: 2},
		{name: "agent not found", args: submitArgs("hi"), code: 1},
		{name: "agent failed", scenario: "exit_nonzero", args: submitArgs("hi"), code: 1},
		{name: "bad output", scenario: "not_json", args: submitArgs("hi"), code: 1},
		{name: "ok", scenario: "ok", args: submitArgs("hi"), code: 0},

		{name: "task create", scenario: "ok", args: []string{"task", "create", "--agent", "claude", "--cwd", "{DIR}", "hi"},
			result: func(t *testing.T, stdout string, v map[string]string) {
				r := decodeOne[struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				}](t, stdout)
				if !taskID.MatchString(r.ID) || r.Status != "queued" {
					t.Errorf("--json: %+v", r)
				}
			}, stdout: "{ID}\n"},
		{name: "task create without agent", scenario: "ok", args: []string{"task", "create", "--cwd", "{DIR}", "hi"}, code: 2,
			errMsg: "agent is required (one of: claude, codex)"},
		{name: "task create internal", scenario: "ok", args: []string{"task", "create", "--agent", "claude", "--cwd", "{DIR}", "hi"}, code: 1,
			setup: homeIsFile},

		{name: "task list empty", scenario: "ok", args: []string{"task", "list"}, result: jsonIs(`{"tasks":[]}`)},
		{name: "task list", scenario: "ok", args: []string{"task", "list"}, setup: queued,
			result: func(t *testing.T, stdout string, v map[string]string) {
				r := decodeOne[struct {
					Tasks []struct {
						ID        string `json:"id"`
						Status    string `json:"status"`
						Agent     string `json:"agent"`
						Cwd       string `json:"cwd"`
						CreatedAt string `json:"created_at"`
					} `json:"tasks"`
				}](t, stdout)
				if len(r.Tasks) != 1 || r.Tasks[0].ID != v["ID"] || r.Tasks[0].Status != "queued" || r.Tasks[0].Agent != "claude" ||
					r.Tasks[0].Cwd != v["DIR"] || r.Tasks[0].CreatedAt == "" {
					t.Errorf("--json: %+v", r)
				}
			}, stdout: "{ID}  queued  claude  {CREATED_AT}\n"},
		{name: "task list with an argument", scenario: "ok", args: []string{"task", "list", "extra"}, code: 2, errMsg: "list takes no arguments"},
		{name: "task list internal", scenario: "ok", args: []string{"task", "list"}, code: 1, setup: homeIsFile},

		{name: "task show queued", scenario: "ok", args: []string{"task", "show", "{ID}"}, setup: queued,
			result: taskIs("queued", ""), stdout: "{ID}  queued  claude  {DIR}\n"},
		{name: "task show done", scenario: "ok", args: []string{"task", "show", "{ID}"}, setup: worked,
			result: taskIs("done", "ok"), stdout: "{ID}  done  claude  {DIR}\nfake answer\n", stderr: "session: sess-fake-1\n"},
		{name: "task show failed", scenario: "exit_nonzero", args: []string{"task", "show", "{ID}"}, setup: worked,
			result: taskIs("failed", "agent_failed"), stdout: "{ID}  failed  claude  {DIR}\n",
			stderr: "session: sess-fake-1\naits: task {ID} failed: agent_failed: the agent exited with code 3\n"},
		{name: "task show unknown", scenario: "ok", args: []string{"task", "show", "20000101T000000-000000"}, code: 2,
			errMsg: "no task 20000101T000000-000000"},
		{name: "task show internal", scenario: "ok", args: []string{"task", "show", "20000101T000000-000000"}, code: 1, setup: homeIsFile},

		{name: "task work empty", scenario: "ok", args: []string{"task", "work"}, result: jsonIs(`{"status":"empty"}`)},
		{name: "task work done", scenario: "ok", args: []string{"task", "work"}, setup: queued,
			result: taskIs("done", "ok"), stdout: "{ID}  done  claude  {DIR}\nfake answer\n", stderr: "session: sess-fake-1\n"},
		{name: "task work failed", scenario: "exit_nonzero", args: []string{"task", "work"}, code: 1, setup: queued,
			result: taskIs("failed", "agent_failed"), stdout: "{ID}  failed  claude  {DIR}\n",
			stderr: "session: sess-fake-1\naits: task {ID} failed: agent_failed: the agent exited with code 3\n"},
		{name: "task work not started", args: []string{"task", "work"}, code: 1, setup: queued,
			result: taskIs("failed", "not_started"), stdout: "{ID}  failed  claude  {DIR}\n",
			stderr: "aits: task {ID} failed: not_started: agent executable not found: claude is not on PATH\n"},
		{name: "task work with an argument", scenario: "ok", args: []string{"task", "work", "extra"}, code: 2, errMsg: "work takes no arguments"},
		{name: "task work empty agent", scenario: "ok", args: []string{"task", "work", "--agent="}, code: 2},
		{name: "task work internal", scenario: "ok", args: []string{"task", "work"}, code: 1, setup: homeIsFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.args[0] == "task" {
				checkTask(t, tt)
				return
			}
			path := func() string {
				if tt.scenario == "" {
					return ""
				}
				return vendor(t, tt.scenario)
			}
			out := call{path: path(), args: append(tt.args, "--json")}.run(t)
			r := decode(t, out.stdout)
			if out.code != tt.code || out.stderr != "" || (r.Error == nil) == (r.Status == "") {
				t.Errorf("--json: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
			}
			out = call{path: path(), args: tt.args}.run(t)
			if out.code != tt.code {
				t.Errorf("text: code=%d, want %d", out.code, tt.code)
			}
			if tt.code == 0 {
				if out.stdout != "fake answer\n" {
					t.Errorf("text: stdout=%q", out.stdout)
				}
			} else if out.stdout != "" || !strings.HasPrefix(out.stderr, "aits: ") {
				t.Errorf("text: stdout=%q stderr=%q", out.stdout, out.stderr)
			}
		})
	}
}

// checkTask runs a task row with --json and in text mode, each time on the
// state its setup rebuilds in the row's one AITS_HOME.
func checkTask(t *testing.T, tt outputCase) {
	q := queue{t: t, home: t.TempDir()}
	if tt.scenario != "" {
		q.path = vendor(t, tt.scenario)
	}
	dir := t.TempDir()
	var envelope struct {
		Error *errorBody `json:"error"`
	}
	for _, asJSON := range []bool{true, false} {
		if err := os.RemoveAll(q.home); err != nil {
			t.Fatal(err)
		}
		v := map[string]string{"DIR": dir}
		if tt.setup != nil {
			for k, val := range tt.setup(q, dir) {
				v[k] = val
			}
		}
		args := fill(tt.args[1:], v)
		if asJSON {
			args = append(args, "--json")
		}
		out := q.run(args...)
		if _, ok := v["ID"]; !ok && strings.Contains(tt.stdout, "{ID}") {
			v["ID"] = onlyTask(q)
		}
		if out.code != tt.code {
			t.Errorf("json=%v: code=%d, want %d; stdout=%q stderr=%q", asJSON, out.code, tt.code, out.stdout, out.stderr)
		}
		switch {
		case asJSON && out.stderr != "":
			t.Errorf("--json: stderr=%q", out.stderr)
		case asJSON && tt.result == nil:
			envelope = decodeOne[struct {
				Error *errorBody `json:"error"`
			}](t, out.stdout)
			if e := envelope.Error; e == nil || e.Code != map[int]string{1: "internal", 2: "invalid_input"}[tt.code] ||
				(tt.errMsg != "" && e.Message != tt.errMsg) {
				t.Errorf("--json: error %+v", e)
			}
		case asJSON:
			tt.result(t, out.stdout, v)
		case tt.result == nil:
			if out.stdout != "" || envelope.Error == nil || out.stderr != "aits: "+envelope.Error.Message+"\n" {
				t.Errorf("text: stdout=%q stderr=%q", out.stdout, out.stderr)
			}
		default:
			if want := fill1(tt.stdout, v); out.stdout != want {
				t.Errorf("text: stdout=%q, want %q", out.stdout, want)
			}
			if want := fill1(tt.stderr, v); out.stderr != want {
				t.Errorf("text: stderr=%q, want %q", out.stderr, want)
			}
		}
	}
}

var taskID = regexp.MustCompile(`^\d{8}T\d{6}-[0-9a-f]{6}$`)

// queued creates one task for claude in dir and returns its id and creation
// time as list prints it.
func queued(q queue, dir string) map[string]string {
	q.t.Helper()
	out := q.run("create", "--agent", "claude", "--cwd", dir, "--json", "hi")
	var r struct{ ID string }
	if err := json.Unmarshal([]byte(out.stdout), &r); out.code != 0 || err != nil {
		q.t.Fatalf("create: code=%d stdout=%q", out.code, out.stdout)
	}
	s := decodeOne[taskRecord](q.t, q.run("show", r.ID, "--json").stdout)
	created, err := time.Parse(time.RFC3339Nano, s.CreatedAt)
	if err != nil {
		q.t.Fatal(err)
	}
	return map[string]string{"ID": r.ID, "CREATED_AT": created.Format(time.RFC3339)}
}

// worked is queued, then worked once with the row's scenario.
func worked(q queue, dir string) map[string]string {
	v := queued(q, dir)
	q.run("work")
	return v
}

// homeIsFile makes AITS_HOME a regular file, so storage fails.
func homeIsFile(q queue, _ string) map[string]string {
	if err := os.WriteFile(q.home, nil, 0o600); err != nil {
		q.t.Fatal(err)
	}
	return nil
}

// onlyTask returns the id of the one task in q.
func onlyTask(q queue) string {
	q.t.Helper()
	var r struct{ Tasks []struct{ ID string } }
	if err := json.Unmarshal([]byte(q.run("list", "--json").stdout), &r); err != nil || len(r.Tasks) != 1 {
		q.t.Fatalf("list: %v %+v", err, r)
	}
	return r.Tasks[0].ID
}

// taskIs checks a --json task record of status, whose run has runStatus
// ("" for no run).
func taskIs(status, runStatus string) func(*testing.T, string, map[string]string) {
	return func(t *testing.T, stdout string, v map[string]string) {
		r := decodeOne[taskRecord](t, stdout)
		got := ""
		if r.Run != nil {
			got = r.Run.Status
		}
		if r.ID != v["ID"] || r.Status != status || r.Cwd != v["DIR"] || got != runStatus {
			t.Errorf("--json: %+v", r)
		}
	}
}

// jsonIs checks that stdout is exactly want and a newline; text mode prints
// nothing.
func jsonIs(want string) func(*testing.T, string, map[string]string) {
	return func(t *testing.T, stdout string, _ map[string]string) {
		if stdout != want+"\n" {
			t.Errorf("--json: stdout=%q, want %q", stdout, want)
		}
	}
}

func fill(args []string, v map[string]string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = fill1(a, v)
	}
	return out
}

func fill1(s string, v map[string]string) string {
	for k, val := range v {
		s = strings.ReplaceAll(s, "{"+k+"}", val)
	}
	return s
}
