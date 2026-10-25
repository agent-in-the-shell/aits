package main

import (
	"slices"
	"strings"
	"testing"
)

// modelArgv and modelResumeArgv are the argv each agent gets with --model
// sonnet, for a new conversation and to continue session sess-fake-1 (#119).
var (
	modelArgv = map[string][]string{
		"claude": {"-p", "--output-format", "stream-json", "--verbose", "--model", "sonnet"},
		"codex":  {"exec", "--json", "-s", "read-only", "-m", "sonnet", "-"},
	}
	modelResumeArgv = map[string][]string{
		"claude": {"-p", "--output-format", "stream-json", "--verbose", "--model", "sonnet", "--resume", "sess-fake-1"},
		"codex":  {"exec", "--json", "-s", "read-only", "-m", "sonnet", "resume", "sess-fake-1", "-"},
	}
)

// --model reaches each agent in its own spelling, new or resumed; without it
// the argv is unchanged (TestSubmitResume, TestWriteNotFromEnvironment).
func TestSubmitModel(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		for _, tt := range []struct {
			extra []string
			want  map[string][]string
		}{{nil, modelArgv}, {[]string{"--session", "sess-fake-1"}, modelResumeArgv}} {
			bin := vendorAs(t, agent, "ok")
			out := call{path: bin, args: append(agentArgs(agent, "--model", "sonnet", "--json", "hi"), tt.extra...)}.run(t)
			if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" {
				t.Fatalf("%q: code=%d result=%+v", tt.extra, out.code, r)
			}
			if rec := readRecord(t, bin); !slices.Equal(rec.Args, tt.want[agent]) {
				t.Errorf("%q: agent argv = %q, want %q", tt.extra, rec.Args, tt.want[agent])
			}
		}
	})
}

// A model name that could be read as a flag or split is refused before
// anything starts, by shell submit and task create (P8).
func TestModelInvalid(t *testing.T) {
	for _, model := range []string{"", "-x", "--dangerously-skip-permissions", "son net", "sonnet\n", "a\tb", "a\x1bb", "a b"} {
		bin := vendor(t, "ok")
		out := call{path: bin, args: agentArgs("claude", "--model", model, "--json", "hi")}.run(t)
		if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "invalid_input" {
			t.Errorf("submit --model %q: code=%d stdout=%q", model, out.code, out.stdout)
		}
		if ran(bin) {
			t.Errorf("submit --model %q: the agent ran", model)
		}

		q := newQueue(t, "ok")
		out = q.run("create", "--agent", "claude", "--cwd", t.TempDir(), "--model", model, "--json", "hi")
		if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "invalid_input" {
			t.Errorf("create --model %q: code=%d stdout=%q", model, out.code, out.stdout)
		}
		if out := q.run("list", "--json"); strings.Contains(out.stdout, `"id"`) {
			t.Errorf("create --model %q: a task was queued: %s", model, out.stdout)
		}
	}
}

// task create --model stores the model and work runs with it. A follow-up
// keeps its parent's model, unless it gives its own (#119).
func TestTaskModel(t *testing.T) {
	eachAgent(t, func(t *testing.T, agent string) {
		q := queue{t: t, home: t.TempDir(), path: vendorAs(t, agent, "ok")}
		p := created(t, q.run("create", "--agent", agent, "--cwd", t.TempDir(), "--model", "sonnet", "--json", "hi"))
		w := q.run("work", "--json")
		if rec := decodeOne[taskRecord](t, w.stdout); rec.Status != "done" || rec.Model != "sonnet" {
			t.Errorf("work: task=%+v", rec)
		}
		if rec := readRecord(t, q.path); !slices.Equal(rec.Args, modelArgv[agent]) {
			t.Errorf("work: agent argv = %q, want %q", rec.Args, modelArgv[agent])
		}

		f := q.after(p, "and this")
		w = q.run("work", "--json")
		if rec := decodeOne[taskRecord](t, w.stdout); rec.ID != f || rec.Model != "sonnet" {
			t.Errorf("follow-up without --model: task=%+v", rec)
		}
		if rec := readRecord(t, q.path); !slices.Equal(rec.Args, modelResumeArgv[agent]) {
			t.Errorf("follow-up without --model: agent argv = %q, want %q", rec.Args, modelResumeArgv[agent])
		}

		g := q.after(f, "and that", "--model", "opus")
		w = q.run("work", "--json")
		if rec := decodeOne[taskRecord](t, w.stdout); rec.ID != g || rec.Model != "opus" {
			t.Errorf("follow-up with --model: task=%+v", rec)
		}
		want := slices.Clone(modelResumeArgv[agent])
		want[slices.Index(want, "sonnet")] = "opus"
		if rec := readRecord(t, q.path); !slices.Equal(rec.Args, want) {
			t.Errorf("follow-up with --model: agent argv = %q, want %q", rec.Args, want)
		}
	})
}

// A task without --model has no model field, and runs with the vendor's
// default.
func TestTaskNoModel(t *testing.T) {
	q := newQueue(t, "ok")
	created(t, q.run("create", "--agent", "claude", "--cwd", t.TempDir(), "--json", "hi"))
	if w := q.run("work", "--json"); strings.Contains(w.stdout, `"model"`) {
		t.Errorf("work: %s", w.stdout)
	}
}
