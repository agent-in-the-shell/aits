package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// outputCase is one row of TestOutputChannels. Each row runs twice, with
// --json and in text mode, on the state setup rebuilds in the row's one
// AITS_HOME; "{ID}", "{DIR}" and "{CREATED_AT}" stand for that run's values.
type outputCase struct {
	name     string
	scenario string // "" means no agent on PATH
	args     []string
	code     int
	setup    func(q queue, dir string) map[string]string // nil: an empty queue

	errCode string // the --json error envelope's code; "" means a result
	errMsg  string // and its exact message, when set
	result  func(t *testing.T, stdout string, v map[string]string)
	stdout  string // text mode, exact; an error prints only "aits: MESSAGE"
	stderr  string
}

// TestOutputChannels enforces P5 for every kind of outcome of every command:
// with --json, stdout is exactly one JSON object (an error envelope or a
// result) and stderr is empty; in text mode, stdout carries only the result
// and stderr the diagnostics, with an error printed as "aits: MESSAGE".
func TestOutputChannels(t *testing.T) {
	const failed = "aits: task {ID} failed: agent_failed: the agent exited with code 3\n"
	tests := []outputCase{
		{name: "unknown flag", scenario: "ok", args: submitArgs("--bogus", "hi"), code: 2, errCode: "unknown_flag"},
		{name: "unknown command", scenario: "ok", args: []string{"nope"}, code: 2, errCode: "unknown_command"},
		{name: "invalid input", scenario: "ok", args: []string{"shell", "submit", "hi"}, code: 2, errCode: "invalid_input"},
		{name: "agent not found", args: submitArgs("hi"), code: 1, errCode: "agent_not_found"},
		{name: "agent failed", scenario: "exit_nonzero", args: submitArgs("hi"), code: 1, result: submitIs("agent_failed"),
			stderr: "aits: agent_failed: the agent exited with code 3\nsession: sess-fake-1\n"},
		{name: "bad output", scenario: "not_json", args: submitArgs("hi"), code: 1, result: submitIs("bad_agent_output"),
			stderr: "aits: bad_agent_output: the agent printed a line that is not a usable JSON event\nsession: sess-fake-1\n"},
		{name: "ok", scenario: "ok", args: submitArgs("hi"), result: submitIs("ok"), stdout: "fake answer\n", stderr: "session: sess-fake-1\n"},

		{name: "task create", scenario: "ok", args: []string{"task", "create", "--agent", "claude", "--cwd", "{DIR}", "hi"},
			result: createdIs, stdout: "{ID}\n"},
		{name: "task create without agent", scenario: "ok", args: []string{"task", "create", "--cwd", "{DIR}", "hi"}, code: 2,
			errCode: "invalid_input", errMsg: "agent is required (one of: claude, codex)"},
		{name: "task create internal", scenario: "ok", args: []string{"task", "create", "--agent", "claude", "--cwd", "{DIR}", "hi"}, code: 1,
			setup: homeIsFile, errCode: "internal"},

		{name: "task list empty", scenario: "ok", args: []string{"task", "list"}, result: jsonIs(`{"tasks":[]}`)},
		{name: "task list", scenario: "ok", args: []string{"task", "list"}, setup: queued,
			result: listIs, stdout: "{ID}  queued  claude  {CREATED_AT}\n"},
		{name: "task list with an argument", scenario: "ok", args: []string{"task", "list", "extra"}, code: 2,
			errCode: "invalid_input", errMsg: "list takes no arguments"},
		{name: "task list internal", scenario: "ok", args: []string{"task", "list"}, code: 1, setup: homeIsFile, errCode: "internal"},

		{name: "task show queued", scenario: "ok", args: []string{"task", "show", "{ID}"}, setup: queued,
			result: taskIs("queued", ""), stdout: "{ID}  queued  claude  {DIR}\n"},
		{name: "task show done", scenario: "ok", args: []string{"task", "show", "{ID}"}, setup: worked,
			result: taskIs("done", "ok"), stdout: "{ID}  done  claude  {DIR}\nfake answer\n", stderr: "session: sess-fake-1\n"},
		{name: "task show failed", scenario: "exit_nonzero", args: []string{"task", "show", "{ID}"}, setup: worked,
			result: taskIs("failed", "agent_failed"), stdout: "{ID}  failed  claude  {DIR}\n", stderr: "session: sess-fake-1\n" + failed},
		{name: "task show unknown", scenario: "ok", args: []string{"task", "show", "20000101T000000-000000"}, code: 2,
			errCode: "invalid_input", errMsg: "no task 20000101T000000-000000"},
		{name: "task show internal", scenario: "ok", args: []string{"task", "show", "20000101T000000-000000"}, code: 1,
			setup: homeIsFile, errCode: "internal"},

		{name: "task work empty", scenario: "ok", args: []string{"task", "work"}, result: jsonIs(`{"status":"empty"}`)},
		{name: "task work done", scenario: "ok", args: []string{"task", "work"}, setup: queued,
			result: taskIs("done", "ok"), stdout: "{ID}  done  claude  {DIR}\nfake answer\n", stderr: "session: sess-fake-1\n"},
		{name: "task work failed", scenario: "exit_nonzero", args: []string{"task", "work"}, code: 1, setup: queued,
			result: taskIs("failed", "agent_failed"), stdout: "{ID}  failed  claude  {DIR}\n", stderr: "session: sess-fake-1\n" + failed},
		{name: "task work not started", args: []string{"task", "work"}, code: 1, setup: queued,
			result: taskIs("failed", "not_started"), stdout: "{ID}  failed  claude  {DIR}\n",
			stderr: "aits: task {ID} failed: not_started: agent executable not found: claude is not on PATH\n"},
		{name: "task work with an argument", scenario: "ok", args: []string{"task", "work", "extra"}, code: 2,
			errCode: "invalid_input", errMsg: "work takes no arguments"},
		{name: "task work empty agent", scenario: "ok", args: []string{"task", "work", "--agent="}, code: 2, errCode: "invalid_input"},
		{name: "task work internal", scenario: "ok", args: []string{"task", "work"}, code: 1, setup: homeIsFile, errCode: "internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			q, dir := newQueue(t, tt.scenario), t.TempDir()

			out, v := tt.run(q, dir, "--json")
			if out.code != tt.code || out.stderr != "" {
				t.Errorf("--json: code=%d, want %d; stderr=%q", out.code, tt.code, out.stderr)
			}
			msg := ""
			if tt.errCode != "" {
				e := decode(t, out.stdout).Error
				if e == nil || e.Code != tt.errCode || (tt.errMsg != "" && e.Message != tt.errMsg) {
					t.Fatalf("--json: stdout=%q, want error %s %q", out.stdout, tt.errCode, tt.errMsg)
				}
				msg = e.Message
			} else {
				tt.result(t, out.stdout, v)
			}

			out, v = tt.run(q, dir)
			stdout, stderr := tt.stdout, tt.stderr
			if tt.errCode != "" {
				stdout, stderr = "", "aits: "+msg+"\n"
			}
			r := replacer(v)
			if out.code != tt.code || out.stdout != r.Replace(stdout) || out.stderr != r.Replace(stderr) {
				t.Errorf("text: code=%d stdout=%q stderr=%q; want %d %q %q", out.code, out.stdout, out.stderr, tt.code, r.Replace(stdout), r.Replace(stderr))
			}
		})
	}
}

// run rebuilds the row's state and runs it once with extra arguments. A
// row that creates its task itself (task create) gets its {ID} from the queue.
func (tt outputCase) run(q queue, dir string, extra ...string) (output, map[string]string) {
	q.t.Helper()
	if err := os.RemoveAll(q.home); err != nil {
		q.t.Fatal(err)
	}
	v := map[string]string{"DIR": dir}
	if tt.setup != nil {
		for k, val := range tt.setup(q, dir) {
			v[k] = val
		}
	}
	args := make([]string, len(tt.args), len(tt.args)+len(extra))
	for i, a := range tt.args {
		args[i] = replacer(v).Replace(a)
	}
	out := call{path: q.path, env: []string{"AITS_HOME=" + q.home}, args: append(args, extra...)}.run(q.t)
	if _, ok := v["ID"]; !ok && strings.Contains(tt.stdout, "{ID}") {
		v["ID"] = onlyTask(q)["ID"]
	}
	return out, v
}

func replacer(v map[string]string) *strings.Replacer {
	var pairs []string
	for k, val := range v {
		pairs = append(pairs, "{"+k+"}", val)
	}
	return strings.NewReplacer(pairs...)
}

// queued creates one task for claude in dir.
func queued(q queue, dir string) map[string]string {
	q.t.Helper()
	if out := q.run("create", "--agent", "claude", "--cwd", dir, "hi"); out.code != 0 {
		q.t.Fatalf("create: code=%d stderr=%q", out.code, out.stderr)
	}
	return onlyTask(q)
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

// onlyTask returns the id of the one task in q and its creation time as
// text-mode list prints it.
func onlyTask(q queue) map[string]string {
	q.t.Helper()
	var r struct {
		Tasks []struct {
			ID        string    `json:"id"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(q.run("list", "--json").stdout), &r); err != nil || len(r.Tasks) != 1 {
		q.t.Fatalf("list: %v %+v", err, r)
	}
	return map[string]string{"ID": r.Tasks[0].ID, "CREATED_AT": r.Tasks[0].CreatedAt.Format(time.RFC3339)}
}

func submitIs(status string) func(*testing.T, string, map[string]string) {
	return func(t *testing.T, stdout string, _ map[string]string) {
		if r := decode(t, stdout); r.Status != status || r.Error != nil {
			t.Errorf("--json: %+v", r)
		}
	}
}

func createdIs(t *testing.T, stdout string, v map[string]string) {
	r := decodeOne[struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}](t, stdout)
	if r.ID != v["ID"] || r.Status != "queued" {
		t.Errorf("--json: %+v, want id %s", r, v["ID"])
	}
}

func listIs(t *testing.T, stdout string, v map[string]string) {
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

// jsonIs checks that stdout is exactly want and a newline.
func jsonIs(want string) func(*testing.T, string, map[string]string) {
	return func(t *testing.T, stdout string, _ map[string]string) {
		if stdout != want+"\n" {
			t.Errorf("--json: stdout=%q, want %q", stdout, want)
		}
	}
}
