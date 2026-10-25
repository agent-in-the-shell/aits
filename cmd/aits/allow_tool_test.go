package main

import (
	"slices"
	"strings"
	"testing"
)

// allowWarning is what --allow-tool prints on stderr for tool, in text and
// --json mode (P9, #121).
func allowWarning(tool string) string {
	return "aits: warning: --allow-tool lets the agent run " + tool + " without asking\n"
}

// allowArgv is the argv Claude Code gets with --allow-tool Read and
// --allow-tool "Bash(git diff:*)": each tool its own element (P8).
var allowArgv = []string{"-p", "--output-format", "stream-json", "--verbose",
	"--allowedTools", "Read", "--allowedTools", "Bash(git diff:*)"}

// --allow-tool reaches Claude Code as one --allowedTools per tool, new or
// resumed, with or without --write.
func TestSubmitAllowTool(t *testing.T) {
	tools := []string{"--allow-tool", "Read", "--allow-tool", "Bash(git diff:*)"}
	for _, tt := range []struct {
		extra, want []string
	}{
		{nil, allowArgv},
		{[]string{"--session", "sess-fake-1"}, append(slices.Clone(allowArgv), "--resume", "sess-fake-1")},
		{[]string{"--write"}, append(slices.Clone(allowArgv), "--permission-mode", "acceptEdits", "--settings", claudeSandbox)},
	} {
		bin := vendor(t, "ok")
		out := call{path: bin, args: submitArgs(append(append(slices.Clone(tools), tt.extra...), "--json", "hi")...)}.run(t)
		if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" {
			t.Fatalf("%q: code=%d result=%+v", tt.extra, out.code, r)
		}
		if rec := readRecord(t, bin); !slices.Equal(rec.Args, tt.want) {
			t.Errorf("%q: agent argv = %q, want %q", tt.extra, rec.Args, tt.want)
		}
	}
}

// A tool that could be read as a flag or as a list of tools is refused
// before anything starts, by shell submit and task create; so is any tool for
// codex (P6, P8).
func TestAllowToolInvalid(t *testing.T) {
	for _, tool := range []string{"", "-x", "--dangerously-skip-permissions", "Read,Edit", "Read\nEdit", "a\x1bb"} {
		bin := vendor(t, "ok")
		out := call{path: bin, args: submitArgs("--allow-tool", tool, "--json", "hi")}.run(t)
		if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "invalid_input" || out.stderr != "" {
			t.Errorf("submit --allow-tool %q: code=%d stdout=%q stderr=%q", tool, out.code, out.stdout, out.stderr)
		}
		if ran(bin) {
			t.Errorf("submit --allow-tool %q: the agent ran", tool)
		}

		q := newQueue(t, "ok")
		out = q.run("create", "--agent", "claude", "--cwd", t.TempDir(), "--allow-tool", tool, "--json", "hi")
		if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "invalid_input" || out.stderr != "" {
			t.Errorf("create --allow-tool %q: code=%d stdout=%q stderr=%q", tool, out.code, out.stdout, out.stderr)
		}
	}

	bin := vendorAs(t, "codex", "ok")
	if out := (call{path: bin, args: agentArgs("codex", "--allow-tool", "Read", "--json", "hi")}.run(t)); out.code != 2 || ran(bin) {
		t.Errorf("codex submit: code=%d stdout=%q", out.code, out.stdout)
	}
	q := queue{t: t, home: t.TempDir(), path: vendorAs(t, "codex", "ok")}
	p := created(t, q.run("create", "--agent", "codex", "--cwd", t.TempDir(), "--json", "hi"))
	q.run("work")
	if out := q.run("create", "--after", p, "--allow-tool", "Read", "--json", "and this"); out.code != 2 {
		t.Errorf("codex follow-up: code=%d stdout=%q", out.code, out.stdout)
	}
}

// No environment variable grants a tool (P9).
func TestAllowToolNotFromEnvironment(t *testing.T) {
	env := []string{"AITS_ALLOW_TOOL=Read", "AITS_ALLOW_TOOLS=Read", "AITS_SHELL_ALLOW_TOOL=Read", "ALLOWED_TOOLS=Read"}
	bin := vendor(t, "ok")
	out := call{path: bin, env: env, args: submitArgs("--json", "hi")}.run(t)
	if rec := readRecord(t, bin); out.code != 0 || out.stderr != "" || !slices.Equal(rec.Args, agentArgv["claude"]) {
		t.Errorf("submit: code=%d stderr=%q argv=%q", out.code, out.stderr, rec.Args)
	}
}

// task create --allow-tool stores the tools and work grants them without
// warning again; a follow-up takes only its own (#121).
func TestTaskAllowTool(t *testing.T) {
	q := newQueue(t, "ok")
	p := created(t, q.run("create", "--agent", "claude", "--cwd", t.TempDir(),
		"--allow-tool", "Read", "--allow-tool", "Bash(git diff:*)", "--json", "hi"))
	w := q.run("work", "--json")
	if rec := decodeOne[taskRecord](t, w.stdout); rec.Status != "done" || !slices.Equal(rec.AllowTools, []string{"Read", "Bash(git diff:*)"}) || w.stderr != "" {
		t.Errorf("work: task=%+v stderr=%q", rec, w.stderr)
	}
	if rec := readRecord(t, q.path); !slices.Equal(rec.Args, allowArgv) {
		t.Errorf("work: agent argv = %q, want %q", rec.Args, allowArgv)
	}

	f := q.after(p, "and this")
	w = q.run("work", "--json")
	if strings.Contains(w.stdout, `"allow_tools"`) {
		t.Errorf("follow-up without --allow-tool: %s", w.stdout)
	}
	if rec := readRecord(t, q.path); !slices.Equal(rec.Args, resumeArgv["claude"]) {
		t.Errorf("follow-up without --allow-tool: agent argv = %q, want %q", rec.Args, resumeArgv["claude"])
	}

	q.after(f, "and that", "--allow-tool", "Edit")
	q.run("work")
	want := []string{"-p", "--output-format", "stream-json", "--verbose", "--allowedTools", "Edit", "--resume", "sess-fake-1"}
	if rec := readRecord(t, q.path); !slices.Equal(rec.Args, want) {
		t.Errorf("follow-up with --allow-tool: agent argv = %q, want %q", rec.Args, want)
	}
}
