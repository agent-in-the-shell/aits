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

// --write starts each agent in its vendor's sandbox, new or resumed (#104).
// The warning is checked by TestOutputChannels.
func TestSubmitWrite(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		for _, tt := range []struct {
			extra []string
			want  map[string][]string
		}{{nil, writeArgv}, {[]string{"--session", "sess-fake-1"}, writeResumeArgv}} {
			bin := vendorAs(t, agent, "ok")
			out := call{path: bin, args: append(agentArgs(agent, "--write", "--json", "edit it"), tt.extra...)}.run(t)
			if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" {
				t.Fatalf("%q: code=%d result=%+v", tt.extra, out.code, r)
			}
			if rec := readRecord(t, bin); !slices.Equal(rec.Args, tt.want[agent]) {
				t.Errorf("%q: agent argv = %q, want %q", tt.extra, rec.Args, tt.want[agent])
			}
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

		q := queue{t: t, home: t.TempDir(), path: vendorAs(t, agent, "ok"), env: env}
		out = q.run("create", "--agent", agent, "--cwd", t.TempDir(), "--json", "hi")
		if out.stderr != "" {
			t.Errorf("create: stderr=%q", out.stderr)
		}
		created(t, out)
		q.run("work")
		if rec := readRecord(t, q.path); !slices.Equal(rec.Args, agentArgv[agent]) {
			t.Errorf("work: argv=%q, want %q", rec.Args, agentArgv[agent])
		}
	})
}

// task create --write is the invocation that grants it: the task records
// write, and work runs the agent in its sandbox without warning again. A
// follow-up takes its own --write, never its parent's (#104). The warning of
// create is checked by TestOutputChannels.
func TestTaskWrite(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		q := queue{t: t, home: t.TempDir(), path: vendorAs(t, agent, "ok")}
		p := created(t, q.run("create", "--agent", agent, "--cwd", t.TempDir(), "--write", "--json", "edit it"))
		w := q.run("work", "--json")
		if rec := decodeOne[taskRecord](t, w.stdout); rec.Status != "done" || !rec.Write || w.stderr != "" {
			t.Errorf("work: task=%+v stderr=%q", rec, w.stderr)
		}
		if rec := readRecord(t, q.path); !slices.Equal(rec.Args, writeArgv[agent]) {
			t.Errorf("work: agent argv = %q, want %q", rec.Args, writeArgv[agent])
		}

		// A follow-up without --write resumes without it, and has no write
		// field at all. Its parent's session is sess-fake-1.
		f := q.after(p, "and this")
		w = q.run("work", "--json")
		if strings.Contains(w.stdout, `"write"`) {
			t.Errorf("follow-up without --write: %s", w.stdout)
		}
		if rec := readRecord(t, q.path); !slices.Equal(rec.Args, resumeArgv[agent]) {
			t.Errorf("follow-up without --write: agent argv = %q, want %q", rec.Args, resumeArgv[agent])
		}

		q.after(f, "and that", "--write")
		q.run("work")
		if rec := readRecord(t, q.path); !slices.Equal(rec.Args, writeResumeArgv[agent]) {
			t.Errorf("follow-up with --write: agent argv = %q, want %q", rec.Args, writeResumeArgv[agent])
		}
	})
}

// --write belongs to the invocation that queues the task: work has no such
// flag.
func TestTaskWorkHasNoWrite(t *testing.T) {
	out := newQueue(t, "ok").run("work", "--write", "--json")
	if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "unknown_flag" {
		t.Errorf("work --write: code=%d stdout=%q", out.code, out.stdout)
	}
}
