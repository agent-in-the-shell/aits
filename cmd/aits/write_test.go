package main

import (
	"slices"
	"strings"
	"testing"
)

// writeWarning is what --write prints on stderr, in text and --json mode (P9).
const writeWarning = "aits: warning: --write lets the agent edit files and run commands in its working directory without asking\n"

// claudeSandbox is the one --settings argument --write gives Claude Code.
const claudeSandbox = `{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true,"allowUnsandboxedCommands":false,"failIfUnavailable":true}}`

// writeArgv and writeResumeArgv are the argv each agent gets with --write, for
// a new conversation and to continue session sess-fake-1 (#104).
var (
	writeArgv = map[string][]string{
		"claude": {"-p", "--output-format", "stream-json", "--verbose", "--permission-mode", "acceptEdits", "--settings", claudeSandbox},
		"codex":  {"exec", "--json", "-s", "workspace-write", "-"},
	}
	writeResumeArgv = map[string][]string{
		"claude": {"-p", "--output-format", "stream-json", "--verbose", "--permission-mode", "acceptEdits", "--settings", claudeSandbox, "--resume", "sess-fake-1"},
		"codex":  {"exec", "--json", "-s", "workspace-write", "resume", "sess-fake-1", "-"},
	}
)

// --write starts each agent in its vendor's sandbox, new or resumed, and
// warns on stderr in both output modes (#104).
func TestSubmitWrite(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		for _, tt := range []struct {
			name  string
			extra []string
			want  map[string][]string
		}{
			{"new", nil, writeArgv},
			{"resumed", []string{"--session", "sess-fake-1"}, writeResumeArgv},
		} {
			for _, mode := range []string{"json", "text"} {
				bin := vendorAs(t, agent, "ok")
				args := append(agentArgs(agent, "--write"), tt.extra...)
				if mode == "json" {
					args = append(args, "--json")
				}
				out := call{path: bin, args: append(args, "edit it")}.run(t)
				if out.code != 0 {
					t.Fatalf("%s %s: code=%d stdout=%q stderr=%q", tt.name, mode, out.code, out.stdout, out.stderr)
				}
				if mode == "json" {
					if r := decode(t, out.stdout); r.Status != "ok" || out.stderr != writeWarning {
						t.Errorf("%s json: result=%+v stderr=%q", tt.name, r, out.stderr)
					}
				} else if want := writeWarning + "session: sess-fake-1\n"; out.stdout != "fake answer\n" || out.stderr != want {
					t.Errorf("%s text: stdout=%q stderr=%q, want stderr %q", tt.name, out.stdout, out.stderr, want)
				}
				if rec := readRecord(t, bin); !slices.Equal(rec.Args, tt.want[agent]) {
					t.Errorf("%s %s: agent argv = %q, want %q", tt.name, mode, rec.Args, tt.want[agent])
				}
			}
		}
	})
}

// --write is a boolean flag: false is the same as leaving it out, anything
// else is invalid input; nothing starts and no warning is printed (P6).
func TestSubmitWriteValue(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		bin := vendorAs(t, agent, "ok")
		out := call{path: bin, args: agentArgs(agent, "--write=false", "--json", "hi")}.run(t)
		if rec := readRecord(t, bin); out.code != 0 || out.stderr != "" || !slices.Equal(rec.Args, agentArgv[agent]) {
			t.Errorf("--write=false: code=%d stderr=%q argv=%q", out.code, out.stderr, rec.Args)
		}

		bin = vendorAs(t, agent, "ok")
		out = call{path: bin, args: agentArgs(agent, "--write=abc", "--json", "hi")}.run(t)
		if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "invalid_input" || out.stderr != "" || ran(bin) {
			t.Errorf("--write=abc: code=%d stdout=%q stderr=%q ran=%v", out.code, out.stdout, out.stderr, ran(bin))
		}
	})
}

// No environment variable turns --write on, for shell submit or task create
// (P9).
func TestWriteNotFromEnvironment(t *testing.T) {
	env := []string{"AITS_WRITE=1", "AITS_SHELL_WRITE=1", "AITS_TASK_WRITE=1", "WRITE=1"}
	eachAgent(t, func(t *testing.T, agent string) {
		bin := vendorAs(t, agent, "ok")
		out := call{path: bin, env: env, args: agentArgs(agent, "--json", "hi")}.run(t)
		if rec := readRecord(t, bin); out.code != 0 || out.stderr != "" || !slices.Equal(rec.Args, agentArgv[agent]) {
			t.Errorf("submit: code=%d stderr=%q argv=%q, want %q", out.code, out.stderr, rec.Args, agentArgv[agent])
		}

		q := queue{t: t, home: t.TempDir(), path: vendorAs(t, agent, "ok")}
		withEnv := func(args ...string) output {
			return call{path: q.path, env: append([]string{"AITS_HOME=" + q.home}, env...), args: append([]string{"task"}, args...)}.run(t)
		}
		out = withEnv("create", "--agent", agent, "--cwd", t.TempDir(), "--json", "hi")
		if out.stderr != "" {
			t.Errorf("create: stderr=%q", out.stderr)
		}
		created(t, out)
		withEnv("work")
		if rec := readRecord(t, q.path); !slices.Equal(rec.Args, agentArgv[agent]) {
			t.Errorf("work: argv=%q, want %q", rec.Args, agentArgv[agent])
		}
	})
}

// task create --write is the invocation that grants it: the task records
// write, create warns, and work runs the agent in its sandbox without
// warning again. A follow-up takes its own --write, never its parent's (#104).
func TestTaskWrite(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		q := queue{t: t, home: t.TempDir(), path: vendorAs(t, agent, "ok")}
		out := q.run("create", "--agent", agent, "--cwd", t.TempDir(), "--write", "--json", "edit it")
		if out.stderr != writeWarning {
			t.Errorf("create --write: stderr=%q", out.stderr)
		}
		p := created(t, out)
		if rec := decodeOne[taskRecord](t, q.run("show", p, "--json").stdout); !rec.Write {
			t.Errorf("task = %+v, want write", rec)
		}
		w := q.run("work", "--json")
		if rec := decodeOne[taskRecord](t, w.stdout); rec.Status != "done" || !rec.Write || w.stderr != "" {
			t.Errorf("work: task=%+v stderr=%q", rec, w.stderr)
		}
		if rec := readRecord(t, q.path); !slices.Equal(rec.Args, writeArgv[agent]) {
			t.Errorf("work: agent argv = %q, want %q", rec.Args, writeArgv[agent])
		}

		// A follow-up without --write resumes without it. Its parent's session
		// is sess-fake-1, so the resumed argv is the plain one.
		f := q.after(p, "and this")
		if rec := decodeOne[taskRecord](t, q.run("show", f, "--json").stdout); rec.Write {
			t.Errorf("follow-up without --write = %+v", rec)
		}
		q.run("work")
		if rec := readRecord(t, q.path); !slices.Equal(rec.Args, resumeArgv[agent]) {
			t.Errorf("follow-up without --write: agent argv = %q, want %q", rec.Args, resumeArgv[agent])
		}

		out = q.run("create", "--after", f, "--write", "--json", "and that")
		if out.stderr != writeWarning {
			t.Errorf("follow-up create --write: stderr=%q", out.stderr)
		}
		created(t, out)
		q.run("work")
		if rec := readRecord(t, q.path); !slices.Equal(rec.Args, writeResumeArgv[agent]) {
			t.Errorf("follow-up with --write: agent argv = %q, want %q", rec.Args, writeResumeArgv[agent])
		}
	})
}

// --write belongs to the invocation that queues the task; work has no such
// flag, and a task created without it has no write field at all.
func TestTaskWorkHasNoWrite(t *testing.T) {
	q := newQueue(t, "ok")
	id := q.create("hi")
	if out := q.run("show", id, "--json"); strings.Contains(out.stdout, `"write"`) {
		t.Errorf("task = %s", out.stdout)
	}
	out := q.run("work", "--write", "--json")
	if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "unknown_flag" {
		t.Errorf("work --write: code=%d stdout=%q", out.code, out.stdout)
	}
}
