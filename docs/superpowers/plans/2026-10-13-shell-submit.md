# `aits shell submit` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `aits shell submit`: run one prompt through Claude Code and return the answer and session id, with the Go module, the shared CLI grammar, CI, and the P3/P4/P5/P12/P13 guards.

**Architecture:** `cmd/aits` is only `main`. `internal/cli` owns the grammar, the command tree, help, and the output/exit-code rules, and adapts flags to the `internal/shell` Go API. `internal/shell` validates a request, starts `claude -p --output-format stream-json --verbose` in its own process group with a built-from-scratch environment and the prompt on stdin, reads events line by line, and maps what it saw to a status. Functional tests build the real binary and a fake `claude`.

**Tech Stack:** Go 1.26 standard library only; GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-10-13-shell-submit-design.md` (issue #2)

## Global Constraints

- Module `github.com/agent-in-the-shell/aits`, `go 1.26`, no third-party modules, builds with `CGO_ENABLED=0` (P3).
- Linux and macOS only; `syscall.Setpgid`, `syscall.Kill` are used directly.
- Exit codes: 0 success, 1 runtime failure, 2 invalid input (P5).
- Error envelope with `--json`: `{"error":{"code":"…","message":"…"}}` on stdout; without `--json`: `aits: <message>` on stderr (P5).
- Error codes used: `unknown_command`, `unknown_flag`, `invalid_input`, `agent_not_found`, `agent_start_failed`, `stdin_failed`, `internal`.
- Statuses: `ok`, `agent_failed`, `bad_agent_output`, `output_too_large`, `timeout`, `stopped`.
- Claude argv exactly `-p --output-format stream-json --verbose`; prompt only on stdin (P8).
- Claude env allowlist: `PATH HOME USER LOGNAME LANG TZ TMPDIR HTTPS_PROXY HTTP_PROXY NO_PROXY CLAUDE_CONFIG_DIR ANTHROPIC_API_KEY` and every `LC_*` (P8).
- Limits: 16 MiB per output event; last 4 KiB of stderr; default timeout 30m; SIGTERM→SIGKILL grace 5s (P8, P10).
- Before a leaf command is reached only `--json` and `--help` are accepted (spec §4).
- aits writes no logs.
- Public text must not contain private names: before every commit run
  `git diff --cached | grep -i -F -f "$AITS_LEAK_TERMS"` — it must print nothing.
  `$AITS_LEAK_TERMS` points at the scratchpad term list; never commit that list.
- Commit messages in English, conventional prefix, body `Refs #2`, ending with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01B6kuFwnH7PWEUpuFq1zj3y
  ```
  (Commit commands below show only the subject; add the body and trailers.)

## Review Focus

1. A prompt larger than a pipe buffer (1 MiB on stdin) must reach the agent intact without deadlock — Task 3, `TestSubmitLargePromptFromStdin`.
2. The agent exits 0 but a grandchild keeps stdout open — aits must finish promptly with `ok`, not wait for the timeout, and leave no process behind — Task 4, `TestSubmitLingeringGrandchild`.
3. Ctrl-C (SIGINT) must behave like SIGTERM: `stopped`, group terminated — Task 4, `TestSubmitStopped/interrupt`.
4. A relative `--cwd` resolves against aits's working directory; a missing one is exit 2 with nothing started — Task 3, `TestSubmitRelativeCwd` and the invalid-input table.
5. `--json` after `--` is prompt text, not a flag: output stays text — Task 5, `TestGrammarDoubleDash`.

---

## File Structure

| File | Responsibility |
|---|---|
| `go.mod` | Module declaration |
| `cmd/aits/main.go` | Signal context, call `cli.Run`, `os.Exit` |
| `internal/cli/cli.go` | `Env`, `Error`, `Run`, error reporting, `wantsJSON` |
| `internal/cli/parse.go` | Interspersed flag parsing (`parseArgs`) |
| `internal/cli/command.go` | `Command`, `Root`, `resolve`, `dispatch`, help, `help` command |
| `internal/cli/shell.go` | `shell` group and `submit` command: flags → `shell.Request`, `shell.Result` → output |
| `internal/shell/shell.go` | `Request`, `Result`, `Status`, errors, `validate`, `Submit` |
| `internal/shell/agent.go` | Agent table, `Agents`, child environment |
| `internal/shell/events.go` | stream-json reader |
| `internal/shell/outcome.go` | `observation`, `decide` |
| `internal/shell/process.go` | Process start/supervision, process group, stderr tail |
| `internal/shell/testdata/fakeclaude/main.go` | Fake `claude` for tests |
| `internal/archtest/` | P3 guard |
| `cmd/aits/*_test.go` | Functional tests, P4/P5/P13 guards, live test |
| `.github/workflows/pr.yml` | `test` job |
| `docs/cli.md`, `README.md`, `CONTRIBUTING.md`, `docs/principles.md` | Docs |

---

### Task 1: Module, grammar and command tree

**Files:**
- Create: `go.mod`, `cmd/aits/main.go`, `internal/cli/cli.go`, `internal/cli/parse.go`, `internal/cli/command.go`
- Test: `internal/cli/parse_test.go`, `internal/cli/command_test.go`

**Interfaces:**
- Produces: `cli.Run(ctx context.Context, args []string, env cli.Env) int`; `cli.Env{Stdin io.Reader; Stdout, Stderr io.Writer; Environ []string; JSON bool}`; `cli.Command{Name, Args, Summary string; Children []*Command; Setup func(*flag.FlagSet) Runner}`; `cli.Runner func(ctx, *Env, []string) (int, error)`; `cli.Root() *Command`; unexported `usageError(code, format, ...) *Error`, `failure(code, format, ...) *Error`, `writeJSON(io.Writer, any)`, `exitOK/exitFailure/exitUsage`.

- [ ] **Step 1: Create the module and write the failing tests**

```bash
cat > go.mod <<'EOF'
module github.com/agent-in-the-shell/aits

go 1.26
EOF
mkdir -p cmd/aits internal/cli
```

`internal/cli/parse_test.go`:

```go
package cli

import (
	"errors"
	"slices"
	"testing"
)

// errCode names the outcome of a parse for table comparison.
func errCode(err error) string {
	var e *Error
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errHelp):
		return "help"
	case errors.As(err, &e):
		return e.Code
	}
	return "other: " + err.Error()
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantPos   []string
		wantAgent string
		wantJSON  bool
		wantCode  string
	}{
		{"flags first", []string{"--agent", "claude", "--json", "hi"}, []string{"hi"}, "claude", true, ""},
		{"flags last", []string{"hi", "--agent", "claude", "--json"}, []string{"hi"}, "claude", true, ""},
		{"equals form", []string{"--agent=claude", "hi"}, []string{"hi"}, "claude", false, ""},
		{"single dash", []string{"-agent", "claude", "hi"}, []string{"hi"}, "claude", false, ""},
		{"lone dash is positional", []string{"--agent", "claude", "-"}, []string{"-"}, "claude", false, ""},
		{"double dash ends flags", []string{"--agent", "claude", "--", "--json", "-x"}, []string{"--json", "-x"}, "claude", false, ""},
		{"value may start with a dash", []string{"--agent", "--", "hi"}, []string{"hi"}, "--", false, ""},
		{"unknown flag", []string{"hi", "--nope"}, nil, "", false, "unknown_flag"},
		{"missing value", []string{"hi", "--agent"}, nil, "", false, "invalid_input"},
		{"bad bool value", []string{"--json=maybe"}, nil, "", false, "invalid_input"},
		{"help", []string{"hi", "--help"}, nil, "", false, "help"},
		{"short help", []string{"-h"}, nil, "", false, "help"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := newFlagSet("test")
			agent := fs.String("agent", "", "")
			jsonOut := fs.Bool("json", false, "")
			pos, err := parseArgs(fs, tt.args)
			if got := errCode(err); got != tt.wantCode {
				t.Fatalf("error = %q (%v), want %q", got, err, tt.wantCode)
			}
			if err != nil {
				return
			}
			if !slices.Equal(pos, tt.wantPos) || *agent != tt.wantAgent || *jsonOut != tt.wantJSON {
				t.Errorf("got pos=%q agent=%q json=%v, want pos=%q agent=%q json=%v",
					pos, *agent, *jsonOut, tt.wantPos, tt.wantAgent, tt.wantJSON)
			}
		})
	}
}

func TestWantsJSON(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"--json"}, true},
		{[]string{"x", "-json"}, true},
		{[]string{"--json=false"}, false},
		{[]string{"--json", "--json=false"}, false},
		{[]string{"--", "--json"}, false},
		{[]string{"--jsonx"}, false},
		{[]string{"json"}, false},
	}
	for _, tt := range tests {
		if got := wantsJSON(tt.args); got != tt.want {
			t.Errorf("wantsJSON(%q) = %v, want %v", tt.args, got, tt.want)
		}
	}
}
```

`internal/cli/command_test.go`:

```go
package cli

import (
	"bytes"
	"context"
	"flag"
	"slices"
	"strings"
	"testing"
)

func testTree() *Command {
	leaf := &Command{
		Name:    "leaf",
		Summary: "A leaf.",
		Setup: func(fs *flag.FlagSet) Runner {
			fs.String("opt", "dflt", "Set the option to `NAME`.")
			return nil
		},
	}
	group := &Command{Name: "group", Summary: "A group.", Children: []*Command{leaf}}
	return &Command{Name: "root", Summary: "Root.", Children: []*Command{group}}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantPath []string
		wantRest []string
		wantCode string
	}{
		{"leaf with args", []string{"group", "leaf", "x", "--opt", "v"}, []string{"root", "group", "leaf"}, []string{"x", "--opt", "v"}, ""},
		{"global flag first", []string{"--json", "group", "leaf", "x"}, []string{"root", "group", "leaf"}, []string{"--json", "x"}, ""},
		{"group only", []string{"group"}, []string{"root", "group"}, nil, ""},
		{"nothing", nil, []string{"root"}, nil, ""},
		{"unknown command", []string{"nope"}, nil, nil, "unknown_command"},
		{"leaf flag before leaf", []string{"group", "--opt", "v", "leaf"}, nil, nil, "unknown_flag"},
		{"command after double dash", []string{"group", "--", "leaf"}, nil, nil, "unknown_command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, path, rest, err := resolve(testTree(), tt.args)
			if got := errCode(err); got != tt.wantCode {
				t.Fatalf("error = %q (%v), want %q", got, err, tt.wantCode)
			}
			if err != nil {
				return
			}
			if !slices.Equal(path, tt.wantPath) || !slices.Equal(rest, tt.wantRest) {
				t.Errorf("got path=%q rest=%q, want path=%q rest=%q", path, rest, tt.wantPath, tt.wantRest)
			}
		})
	}
}

func TestHelpText(t *testing.T) {
	root := testTree()
	group := root.Children[0]
	var b strings.Builder
	writeHelp(&b, []string{"root", "group"}, group)
	wantGroup := "Usage: root group COMMAND\n\nA group.\n\nCommands:\n  leaf  A leaf.\n\nRun 'root group COMMAND --help' for details.\n"
	if b.String() != wantGroup {
		t.Errorf("group help:\n%s\nwant:\n%s", b.String(), wantGroup)
	}
	b.Reset()
	writeHelp(&b, []string{"root", "group", "leaf"}, group.Children[0])
	wantLeaf := "Usage: root group leaf [flags]\n\nA leaf.\n\nFlags:\n" +
		"  --json      Print the result, or the error, as JSON on stdout.\n" +
		"  --opt NAME  Set the option to NAME. (default dflt)\n"
	if b.String() != wantLeaf {
		t.Errorf("leaf help:\n%s\nwant:\n%s", b.String(), wantLeaf)
	}
}

func run(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = Run(context.Background(), args, Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut})
	return code, out.String(), errOut.String()
}

func TestRunChannelsAndExitCodes(t *testing.T) {
	if code, out, errOut := run(); code != 0 || !strings.HasPrefix(out, "Usage: aits COMMAND\n") || errOut != "" {
		t.Errorf("aits: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if code, out, errOut := run("nope"); code != 2 || out != "" || errOut != "aits: unknown command \"nope\" for aits\n" {
		t.Errorf("aits nope: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	wantJSON := `{"error":{"code":"unknown_command","message":"unknown command \"nope\" for aits"}}` + "\n"
	if code, out, errOut := run("nope", "--json"); code != 2 || out != wantJSON || errOut != "" {
		t.Errorf("aits nope --json: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if code, out, _ := run("help", "help"); code != 0 || !strings.HasPrefix(out, "Usage: aits help [flags] [COMMAND...]\n") {
		t.Errorf("aits help help: code=%d stdout=%q", code, out)
	}
	if code, _, _ := run("help", "nope"); code != 2 {
		t.Errorf("aits help nope: code=%d, want 2", code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/`
Expected: FAIL — build errors (`undefined: newFlagSet`, `undefined: parseArgs`, …).

- [ ] **Step 3: Write the implementation**

`internal/cli/cli.go`:

```go
// Package cli implements the aits command line: the shared grammar (P4), the
// command tree and help, and the output and exit-code rules (P5).
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Exit codes (P5).
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// Env is the process environment a command runs in.
type Env struct {
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Environ []string // KEY=VALUE entries, as os.Environ returns them
	JSON    bool     // set from --json before the command runs
}

// Error is a failure reported to the caller under a stable code (P5).
type Error struct {
	Code    string
	Message string
	exit    int
}

func (e *Error) Error() string { return e.Message }

func usageError(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...), exit: exitUsage}
}

func failure(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...), exit: exitFailure}
}

// Run executes one command line (without the program name) and returns the
// process exit code.
func Run(ctx context.Context, args []string, env Env) int {
	env.JSON = wantsJSON(args)
	code, err := dispatch(ctx, Root(), args, &env)
	if err != nil {
		return report(&env, err)
	}
	return code
}

// report writes err to the channel P5 assigns and returns its exit code.
func report(env *Env, err error) int {
	var e *Error
	if !errors.As(err, &e) {
		e = failure("internal", "%v", err)
	}
	if env.JSON {
		writeJSON(env.Stdout, map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
	} else {
		fmt.Fprintf(env.Stderr, "aits: %s\n", e.Message)
	}
	return e.exit
}

func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// wantsJSON reports whether --json is set before "--". It picks the error
// channel for failures that happen before the flags are fully parsed.
func wantsJSON(args []string) bool {
	on := false
	for _, a := range args {
		if a == "--" {
			break
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !isFlag(a) || name != "json" {
			continue
		}
		if !hasValue {
			on = true
		} else if b, err := strconv.ParseBool(value); err == nil {
			on = b
		}
	}
	return on
}
```

`internal/cli/parse.go`:

```go
package cli

import (
	"errors"
	"flag"
	"io"
	"strings"
)

var errHelp = errors.New("help requested")

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

// isFlag reports whether a looks like a flag. A lone "-" is a positional
// argument: it means "read standard input".
func isFlag(a string) bool { return len(a) > 1 && a[0] == '-' }

// parseArgs parses args against fs, allowing flags before, between and after
// positional arguments; "--" ends flag parsing (P4). It returns the
// positional arguments in order.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !isFlag(a) {
			positional = append(positional, a)
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if name == "h" || name == "help" {
			return nil, errHelp
		}
		f := fs.Lookup(name)
		if f == nil {
			return nil, usageError("unknown_flag", "unknown flag %s", a)
		}
		flags = append(flags, a)
		if hasValue || isBoolFlag(f) {
			continue
		}
		if i+1 == len(args) {
			return nil, usageError("invalid_input", "flag %s needs a value", a)
		}
		i++
		flags = append(flags, args[i])
	}
	if err := fs.Parse(flags); err != nil {
		return nil, usageError("invalid_input", "%v", err)
	}
	return positional, nil
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}
```

`internal/cli/command.go`:

```go
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Runner runs a leaf command with its positional arguments. It returns the
// exit code, or an error that Run reports on the error channel (P5).
type Runner func(ctx context.Context, env *Env, args []string) (int, error)

// Command is a node of the command tree: a group (Children) or a leaf (Setup).
type Command struct {
	Name     string
	Args     string // positional synopsis for help, e.g. "(PROMPT | -)"
	Summary  string
	Children []*Command
	// Setup declares the leaf's flags on fs and returns its Runner. It must do
	// nothing else: help calls it to list the flags.
	Setup func(fs *flag.FlagSet) Runner
}

// Root returns the aits command tree.
func Root() *Command {
	root := &Command{
		Name:    "aits",
		Summary: "Run coding agents from the command line.",
	}
	root.Children = append(root.Children, helpCommand(root))
	return root
}

func (c *Command) child(name string) *Command {
	for _, ch := range c.Children {
		if ch.Name == name {
			return ch
		}
	}
	return nil
}

// leafFlags returns the flag set a leaf parses: its own flags plus --json.
func (c *Command) leafFlags(path []string) (*flag.FlagSet, *bool, Runner) {
	fs := newFlagSet(strings.Join(path, " "))
	jsonOut := fs.Bool("json", false, "Print the result, or the error, as JSON on stdout.")
	return fs, jsonOut, c.Setup(fs)
}

// isGlobalFlag reports whether a may appear before the command path reaches a
// leaf: only --json and --help may.
func isGlobalFlag(a string) bool {
	name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
	return name == "json" || name == "help" || name == "h"
}

// resolve walks args down the tree. Command names come first; before a leaf
// is reached only global flags are accepted, because a leaf's flags are
// unknown until the leaf is (spec §4). It returns the command, its path and
// the arguments left for it.
func resolve(root *Command, args []string) (*Command, []string, []string, error) {
	cmd, path := root, []string{root.Name}
	var rest []string
	for i, a := range args {
		if cmd.Setup != nil {
			return cmd, path, append(rest, args[i:]...), nil
		}
		switch {
		case a == "--":
			if i+1 < len(args) {
				return nil, nil, nil, usageError("unknown_command", "unknown command %q for %s", args[i+1], strings.Join(path, " "))
			}
		case isFlag(a):
			if !isGlobalFlag(a) {
				return nil, nil, nil, usageError("unknown_flag", "unknown flag %s for %s", a, strings.Join(path, " "))
			}
			rest = append(rest, a)
		default:
			next := cmd.child(a)
			if next == nil {
				return nil, nil, nil, usageError("unknown_command", "unknown command %q for %s", a, strings.Join(path, " "))
			}
			cmd, path = next, append(path, a)
		}
	}
	return cmd, path, rest, nil
}

func dispatch(ctx context.Context, root *Command, args []string, env *Env) (int, error) {
	cmd, path, rest, err := resolve(root, args)
	if err != nil {
		return 0, err
	}
	if cmd.Setup == nil {
		writeHelp(env.Stdout, path, cmd)
		return exitOK, nil
	}
	fs, jsonOut, run := cmd.leafFlags(path)
	positional, err := parseArgs(fs, rest)
	if err == errHelp {
		writeHelp(env.Stdout, path, cmd)
		return exitOK, nil
	}
	if err != nil {
		return 0, err
	}
	env.JSON = *jsonOut
	return run(ctx, env, positional)
}

// writeHelp prints the help for cmd, reached through path.
func writeHelp(w io.Writer, path []string, cmd *Command) {
	name := strings.Join(path, " ")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if cmd.Setup == nil {
		fmt.Fprintf(tw, "Usage: %s COMMAND\n\n%s\n\nCommands:\n", name, cmd.Summary)
		for _, ch := range cmd.Children {
			fmt.Fprintf(tw, "  %s\t%s\n", ch.Name, ch.Summary)
		}
		fmt.Fprintf(tw, "\nRun '%s COMMAND --help' for details.\n", name)
	} else {
		usage := "Usage: " + name + " [flags]"
		if cmd.Args != "" {
			usage += " " + cmd.Args
		}
		fmt.Fprintf(tw, "%s\n\n%s\n\nFlags:\n", usage, cmd.Summary)
		fs, _, _ := cmd.leafFlags(path)
		fs.VisitAll(func(f *flag.Flag) {
			typ, text := flag.UnquoteUsage(f)
			left := "  --" + f.Name
			if typ != "" {
				left += " " + typ
			}
			fmt.Fprintf(tw, "%s\t%s%s\n", left, text, defaultNote(f))
		})
	}
	_ = tw.Flush()
}

func defaultNote(f *flag.Flag) string {
	switch f.DefValue {
	case "", "false", "0":
		return ""
	}
	return " (default " + f.DefValue + ")"
}

// helpCommand is "aits help [COMMAND...]".
func helpCommand(root *Command) *Command {
	return &Command{
		Name:    "help",
		Args:    "[COMMAND...]",
		Summary: "Show help for aits or one of its commands.",
		Setup: func(*flag.FlagSet) Runner {
			return func(_ context.Context, env *Env, args []string) (int, error) {
				cmd, path, rest, err := resolve(root, args)
				if err != nil {
					return 0, err
				}
				if len(rest) > 0 {
					return 0, usageError("unknown_command", "unknown command %q for %s", rest[0], strings.Join(path, " "))
				}
				writeHelp(env.Stdout, path, cmd)
				return exitOK, nil
			}
		},
	}
}
```

`cmd/aits/main.go`:

```go
// Command aits runs coding agents from the command line.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/agent-in-the-shell/aits/internal/cli"
)

func main() {
	// SIGTERM and SIGINT cancel the context; running commands stop their
	// children and still report a result (P10).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	code := cli.Run(ctx, os.Args[1:], cli.Env{
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Environ: os.Environ(),
	})
	stop()
	os.Exit(code)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `gofmt -l . && go vet ./... && go test ./internal/cli/`
Expected: no gofmt output; `ok  github.com/agent-in-the-shell/aits/internal/cli`.

- [ ] **Step 5: Smoke-test the binary**

Run: `go run ./cmd/aits; echo "exit=$?"; go run ./cmd/aits nope --json; echo "exit=$?"`
Expected: root help then `exit=0`; `{"error":{"code":"unknown_command",…}}` then `exit=2` (go run reports `exit status 2` on stderr as well).

- [ ] **Step 6: Commit**

```bash
git add go.mod cmd/aits/main.go internal/cli
git commit -m "feat(cli): add module, command grammar, help and error channels"
```

---

### Task 2: shell request validation, agent table, event reader, outcome rules

**Files:**
- Create: `internal/shell/shell.go`, `internal/shell/agent.go`, `internal/shell/events.go`, `internal/shell/outcome.go`
- Test: `internal/shell/shell_test.go`, `internal/shell/agent_test.go`, `internal/shell/events_test.go`, `internal/shell/outcome_test.go`

**Interfaces:**
- Produces: `shell.DefaultTimeout`; `shell.Status` + `StatusOK, StatusAgentFailed, StatusBadOutput, StatusTooLarge, StatusTimeout, StatusStopped`; `shell.Request{Agent, Prompt, Dir string; Timeout time.Duration; Environ, Pass []string}`; `shell.Result{Status Status; Agent, SessionID, Answer string; ExitCode int; DurationMS int64; StderrTail, Message string}` (JSON tags per spec §6); `shell.ErrAgentNotFound`; `*shell.InvalidRequestError{Reason string}`; `shell.Agents() []string`; unexported `agent{name, executable string; args, env, envPrefix []string}`, `agents map[string]agent`, `(agent).environ(parent, pass []string) []string`, `lookup(environ []string, name string) (string, bool)`, `(Request).validate() (agent, error)`, `maxEventSize`, `events{sessionID string; result *resultEvent; notJSON, tooLarge bool}`, `resultEvent{IsError bool; Result, SessionID string}`, `readEvents(io.Reader) events`, `observation{stopped, timedOut bool; exitCode int; events events; stderrTail string}`, `decide(observation) (Status, string)`.

- [ ] **Step 1: Write the failing tests**

`internal/shell/shell_test.go`:

```go
package shell

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRequestValidate(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	good := Request{Agent: "claude", Prompt: "hi", Dir: dir, Timeout: time.Minute, Environ: []string{"SET=1"}, Pass: []string{"SET"}}
	if _, err := good.validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	tests := []struct {
		name string
		edit func(*Request)
	}{
		{"no agent", func(r *Request) { r.Agent = "" }},
		{"unknown agent", func(r *Request) { r.Agent = "nope" }},
		{"blank prompt", func(r *Request) { r.Prompt = " \n\t" }},
		{"relative dir", func(r *Request) { r.Dir = "rel" }},
		{"missing dir", func(r *Request) { r.Dir = filepath.Join(dir, "missing") }},
		{"dir is a file", func(r *Request) { r.Dir = file }},
		{"zero timeout", func(r *Request) { r.Timeout = 0 }},
		{"unset pass variable", func(r *Request) { r.Pass = []string{"UNSET"} }},
		{"bad pass name", func(r *Request) { r.Pass = []string{"A=B"} }},
		{"empty pass name", func(r *Request) { r.Pass = []string{""} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := good
			tt.edit(&r)
			_, err := r.validate()
			var invalid *InvalidRequestError
			if !errors.As(err, &invalid) {
				t.Fatalf("validate() = %v, want *InvalidRequestError", err)
			}
		})
	}
}
```

`internal/shell/agent_test.go`:

```go
package shell

import (
	"slices"
	"testing"
)

func TestClaudeEnviron(t *testing.T) {
	parent := []string{
		"PATH=/bin", "HOME=/h", "LC_ALL=C", "ANTHROPIC_API_KEY=k",
		"AITS_CANARY=secret", "GITHUB_TOKEN=t", "EXTRA=e", "NOEQUALS",
	}
	got := agents["claude"].environ(parent, []string{"EXTRA"})
	want := []string{"PATH=/bin", "HOME=/h", "LC_ALL=C", "ANTHROPIC_API_KEY=k", "EXTRA=e"}
	if !slices.Equal(got, want) {
		t.Errorf("environ = %q, want %q", got, want)
	}
}

func TestAgents(t *testing.T) {
	if got := Agents(); !slices.Equal(got, []string{"claude"}) {
		t.Errorf("Agents() = %q", got)
	}
}
```

`internal/shell/events_test.go`:

```go
package shell

import (
	"reflect"
	"strings"
	"testing"
)

func TestReadEvents(t *testing.T) {
	const initLine = `{"type":"system","subtype":"init","session_id":"s1"}`
	const resultLine = `{"type":"result","is_error":false,"result":"the answer","session_id":"s1"}`
	okResult := &resultEvent{Result: "the answer", SessionID: "s1"}
	tests := []struct {
		name string
		in   string
		want events
	}{
		{"stream", initLine + "\n" + `{"type":"assistant","session_id":"s1","message":{}}` + "\n" + resultLine + "\n",
			events{sessionID: "s1", result: okResult}},
		{"first session id wins", initLine + "\n" + `{"type":"result","result":"a","session_id":"s2"}` + "\n",
			events{sessionID: "s1", result: &resultEvent{Result: "a", SessionID: "s2"}}},
		{"session id only on result", `{"type":"result","result":"a","session_id":"s2"}`,
			events{sessionID: "s2", result: &resultEvent{Result: "a", SessionID: "s2"}}},
		{"blank lines ignored", "\n\n" + resultLine + "\n\n", events{sessionID: "s1", result: okResult}},
		{"error result", `{"type":"result","is_error":true,"result":"bad","session_id":"s1"}`,
			events{sessionID: "s1", result: &resultEvent{IsError: true, Result: "bad", SessionID: "s1"}}},
		{"not json", "hello\n" + resultLine, events{notJSON: true}},
		{"json array", "[1,2]\n", events{notJSON: true}},
		{"json null", "null\n", events{notJSON: true}},
		{"result of wrong type", `{"type":"result","result":42}`, events{notJSON: true}},
		{"no result event", initLine, events{sessionID: "s1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := readEvents(strings.NewReader(tt.in)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("readEvents = %+v (result %+v), want %+v (result %+v)", got, got.result, tt.want, tt.want.result)
			}
		})
	}
}

func TestReadEventsSizeLimit(t *testing.T) {
	// line returns a JSON object exactly n bytes long.
	line := func(n int) string {
		const head, tail = `{"type":"assistant","pad":"`, `"}`
		return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
	}
	if ev := readEvents(strings.NewReader(line(maxEventSize) + "\n")); ev.tooLarge || ev.notJSON {
		t.Errorf("a %d-byte event was rejected: tooLarge=%v notJSON=%v", maxEventSize, ev.tooLarge, ev.notJSON)
	}
	if ev := readEvents(strings.NewReader(line(maxEventSize+1) + "\n")); !ev.tooLarge {
		t.Errorf("a %d-byte event was accepted", maxEventSize+1)
	}
}
```

`internal/shell/outcome_test.go`:

```go
package shell

import "testing"

func TestDecide(t *testing.T) {
	okEvents := events{sessionID: "s1", result: &resultEvent{Result: "answer", SessionID: "s1"}}
	tests := []struct {
		name string
		obs  observation
		want Status
	}{
		{"ok", observation{events: okEvents}, StatusOK},
		{"stopped first", observation{stopped: true, timedOut: true, exitCode: -1, events: events{tooLarge: true}}, StatusStopped},
		{"timeout before too large", observation{timedOut: true, exitCode: -1, events: events{tooLarge: true}}, StatusTimeout},
		{"too large before exit code", observation{exitCode: -1, events: events{tooLarge: true}}, StatusTooLarge},
		{"exit code before bad output", observation{exitCode: 3, events: events{notJSON: true}}, StatusAgentFailed},
		{"not json", observation{events: events{notJSON: true}}, StatusBadOutput},
		{"no result", observation{events: events{sessionID: "s1"}}, StatusBadOutput},
		{"error result", observation{events: events{sessionID: "s1", result: &resultEvent{IsError: true, Result: "boom"}}}, StatusAgentFailed},
		{"empty result", observation{events: events{sessionID: "s1", result: &resultEvent{}}}, StatusBadOutput},
		{"no session id", observation{events: events{result: &resultEvent{Result: "answer"}}}, StatusBadOutput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, msg := decide(tt.obs)
			if got != tt.want {
				t.Fatalf("decide = %s, want %s", got, tt.want)
			}
			if (got == StatusOK) != (msg == "") {
				t.Errorf("status %s with message %q", got, msg)
			}
		})
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"one\ntwo", 10, "one"},
		{"abcdef", 3, "abc…"},
		{"héllo", 2, "h…"},
	}
	for _, tt := range tests {
		if got := firstLine(tt.in, tt.max); got != tt.want {
			t.Errorf("firstLine(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/shell/`
Expected: FAIL — build errors (`undefined: Request`, `undefined: agents`, …).

- [ ] **Step 3: Write the implementation**

`internal/shell/shell.go`:

```go
// Package shell runs one prompt through a vendor agent CLI and reports how the
// run ended. It is the Go API other command groups call (P1); it knows nothing
// about flags or output formats.
package shell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultTimeout bounds a run when the caller sets no other timeout.
const DefaultTimeout = 30 * time.Minute

// Status says how a run that started ended (P6: always explicit).
type Status string

const (
	StatusOK          Status = "ok"
	StatusAgentFailed Status = "agent_failed"
	StatusBadOutput   Status = "bad_agent_output"
	StatusTooLarge    Status = "output_too_large"
	StatusTimeout     Status = "timeout"
	StatusStopped     Status = "stopped"
)

// Request is one prompt to run.
type Request struct {
	Agent   string        // one of Agents()
	Prompt  string        // sent on the agent's stdin, never in argv (P8)
	Dir     string        // absolute working directory
	Timeout time.Duration // must be positive
	Environ []string      // the caller's environment, KEY=VALUE
	Pass    []string      // variable names passed from Environ beyond the allowlist (P8)
}

// Result describes a run that started.
type Result struct {
	Status     Status `json:"status"`
	Agent      string `json:"agent"`
	SessionID  string `json:"session_id,omitempty"`
	Answer     string `json:"answer,omitempty"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	StderrTail string `json:"stderr_tail,omitempty"`
	Message    string `json:"message,omitempty"`
}

// ErrAgentNotFound means the agent's executable is not on PATH.
var ErrAgentNotFound = errors.New("agent executable not found")

// InvalidRequestError means the request was rejected before anything started.
type InvalidRequestError struct{ Reason string }

func (e *InvalidRequestError) Error() string { return "invalid request: " + e.Reason }

func invalid(format string, a ...any) error {
	return &InvalidRequestError{Reason: fmt.Sprintf(format, a...)}
}

func (r Request) validate() (agent, error) {
	choices := strings.Join(Agents(), ", ")
	if r.Agent == "" {
		return agent{}, invalid("agent is required (one of: %s)", choices)
	}
	ag, ok := agents[r.Agent]
	if !ok {
		return agent{}, invalid("unknown agent %q (one of: %s)", r.Agent, choices)
	}
	if strings.TrimSpace(r.Prompt) == "" {
		return agent{}, invalid("prompt is empty")
	}
	if !filepath.IsAbs(r.Dir) {
		return agent{}, invalid("working directory %q is not absolute", r.Dir)
	}
	if fi, err := os.Stat(r.Dir); err != nil || !fi.IsDir() {
		return agent{}, invalid("working directory %q is not a directory", r.Dir)
	}
	if r.Timeout <= 0 {
		return agent{}, invalid("timeout must be positive, got %s", r.Timeout)
	}
	for _, name := range r.Pass {
		if name == "" || strings.Contains(name, "=") {
			return agent{}, invalid("%q is not an environment variable name", name)
		}
		if _, ok := lookup(r.Environ, name); !ok {
			return agent{}, invalid("environment variable %s is not set", name)
		}
	}
	return ag, nil
}
```

`internal/shell/agent.go`:

```go
package shell

import (
	"maps"
	"slices"
	"strings"
)

// agent describes how to run one vendor CLI.
type agent struct {
	name       string
	executable string
	args       []string // never carries the prompt (P8)
	env        []string // variables passed through when set
	envPrefix  []string // variables with one of these prefixes are passed through
}

var agents = map[string]agent{
	"claude": {
		name:       "claude",
		executable: "claude",
		args:       []string{"-p", "--output-format", "stream-json", "--verbose"},
		env: []string{
			"PATH", "HOME", "USER", "LOGNAME", "LANG", "TZ", "TMPDIR",
			"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
			"CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY",
		},
		envPrefix: []string{"LC_"},
	},
}

// Agents returns the supported agent names, sorted.
func Agents() []string { return slices.Sorted(maps.Keys(agents)) }

// environ builds the child's environment from scratch (P8): the agent's
// allowlist plus the names in pass, with values taken from parent.
func (a agent) environ(parent, pass []string) []string {
	var out []string
	for _, kv := range parent {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if slices.Contains(a.env, name) || slices.Contains(pass, name) || hasAnyPrefix(name, a.envPrefix) {
			out = append(out, kv)
		}
	}
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// lookup returns the value of name in environ.
func lookup(environ []string, name string) (string, bool) {
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}
```

`internal/shell/events.go`:

```go
package shell

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// maxEventSize bounds one line of agent output (P8).
const maxEventSize = 16 << 20

// events is what aits keeps of the agent's stream-json output: the first
// session id and the result event. Every other event is parsed and dropped.
type events struct {
	sessionID string
	result    *resultEvent
	notJSON   bool
	tooLarge  bool
}

type resultEvent struct {
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
}

// readEvents reads r until EOF, a read error, or the first line longer than
// maxEventSize. It holds at most one line in memory.
func readEvents(r io.Reader) events {
	var ev events
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxEventSize+1)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || ev.notJSON {
			continue // after a bad line keep draining, so the agent is not blocked
		}
		if !ev.add(line) {
			ev.notJSON = true
		}
	}
	ev.tooLarge = errors.Is(sc.Err(), bufio.ErrTooLong)
	return ev
}

// add records one event line and reports whether it was a usable JSON object.
func (ev *events) add(line []byte) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil || fields == nil {
		return false
	}
	if ev.sessionID == "" {
		var id string
		if json.Unmarshal(fields["session_id"], &id) == nil {
			ev.sessionID = id
		}
	}
	var typ string
	if json.Unmarshal(fields["type"], &typ) != nil || typ != "result" {
		return true
	}
	var res resultEvent
	if err := json.Unmarshal(line, &res); err != nil {
		return false
	}
	ev.result = &res
	return true
}
```

`internal/shell/outcome.go`:

```go
package shell

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// observation is everything aits saw of one run.
type observation struct {
	stopped    bool
	timedOut   bool
	exitCode   int
	events     events
	stderrTail string
}

// decide maps an observation to a status and a one-line message, checking the
// conditions in the order of spec §6.
func decide(o observation) (Status, string) {
	ev := o.events
	switch {
	case o.stopped:
		return StatusStopped, "aits was stopped by a signal before the agent finished"
	case o.timedOut:
		return StatusTimeout, "the agent did not finish before the timeout"
	case ev.tooLarge:
		return StatusTooLarge, fmt.Sprintf("an agent output event exceeded %d MiB", maxEventSize>>20)
	case o.exitCode != 0:
		return StatusAgentFailed, fmt.Sprintf("the agent exited with code %d", o.exitCode)
	case ev.notJSON:
		return StatusBadOutput, "the agent printed a line that is not a JSON object"
	case ev.result == nil:
		return StatusBadOutput, "the agent output has no result event"
	case ev.result.IsError:
		return StatusAgentFailed, "the agent reported an error: " + firstLine(ev.result.Result, 200)
	case ev.result.Result == "":
		return StatusBadOutput, "the agent result is empty"
	case ev.sessionID == "":
		return StatusBadOutput, "the agent output has no session id"
	}
	return StatusOK, ""
}

// firstLine returns the first line of s, cut to at most max bytes on a rune
// boundary, with an ellipsis when cut.
func firstLine(s string, max int) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `gofmt -l . && go vet ./... && go test ./internal/shell/`
Expected: no gofmt output; `ok  github.com/agent-in-the-shell/aits/internal/shell`.

- [ ] **Step 5: Commit**

```bash
git add internal/shell
git commit -m "feat(shell): add request validation, claude agent table, event reader and outcome rules"
```

---

### Task 3: `aits shell submit` end to end (happy path, input, environment, agent failures)

**Files:**
- Create: `internal/shell/process.go`, `internal/shell/process_test.go`, `internal/shell/testdata/fakeclaude/main.go`, `internal/cli/shell.go`, `cmd/aits/e2e_test.go`
- Modify: `internal/shell/shell.go` (add `Submit`), `internal/cli/command.go` (`Root` adds `shellGroup()`)

**Interfaces:**
- Consumes: everything Task 1 and Task 2 produce.
- Produces: `shell.Submit(ctx context.Context, req Request) (Result, error)`; unexported `run(ctx, ag agent, path string, req Request) (observation, error)`, `tailBuffer{max int}` with `Write`/`String`, `stderrTailSize`. Test helpers in `cmd/aits` (package `main`): `aitsBin`, `fakeBin`, `vendor(t, scenario) string`, `call{path, dir, stdin string; env, args []string}` with `command(t)` and `run(t) output`, `output{stdout, stderr string; code int}`, `finish(t, err, stdout, stderr) output`, `submitResult`, `decode(t, stdout) submitResult`, `record`, `readRecord(t, dir) record`, `ran(dir) bool`, `submitArgs(extra ...string) []string`. Fake scenarios: `ok`, `exit_nonzero`, `is_error`, `not_json`, `no_result`, `empty_result`, `no_session`, `huge_line`, `sleep`, `ignore_term`, `linger`; fake session id `sess-fake-1`; fake answer `fake answer`.

- [ ] **Step 1: Write the fake vendor**

`internal/shell/testdata/fakeclaude/main.go`:

```go
// Command fakeclaude stands in for the Claude Code CLI in tests (P12).
//
// It reads a scenario name from the file "scenario" next to its executable,
// records what it was given to "record.json" in the same directory, and then
// behaves as the scenario says. The scenario comes from a file, not the
// environment, because aits filters the environment it passes on (P8).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const sessionID = "sess-fake-1"

type record struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
	Dir   string   `json:"dir"`
	Env   []string `json:"env"`
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "--fake-grandchild" {
		writePID(filepath.Join(os.Args[2], "grandchild.pid"))
		time.Sleep(10 * time.Minute)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		fail(err)
	}
	dir := filepath.Dir(exe)
	scenario, err := os.ReadFile(filepath.Join(dir, "scenario"))
	if err != nil {
		fail(err)
	}
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		fail(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	rec, err := json.Marshal(record{Args: os.Args[1:], Stdin: string(stdin), Dir: wd, Env: os.Environ()})
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "record.json"), rec, 0o600); err != nil {
		fail(err)
	}
	play(strings.TrimSpace(string(scenario)), dir, exe)
}

func play(scenario, dir, exe string) {
	switch scenario {
	case "ok":
		emit(initEvent())
		emit(map[string]any{"type": "assistant", "session_id": sessionID, "message": map[string]any{"content": []any{}}})
		emit(result(false, "fake answer"))
	case "exit_nonzero":
		emit(initEvent())
		os.Stderr.Write(bytes.Repeat([]byte("x"), 100<<10))
		os.Stderr.WriteString("\nboom\n")
		os.Exit(3)
	case "is_error":
		emit(initEvent())
		emit(result(true, "fake failure\nsecond line"))
	case "not_json":
		fmt.Println("this is not json")
		emit(initEvent())
		emit(result(false, "fake answer"))
	case "no_result":
		emit(initEvent())
	case "empty_result":
		emit(initEvent())
		emit(result(false, ""))
	case "no_session":
		emit(map[string]any{"type": "result", "is_error": false, "result": "fake answer"})
	case "huge_line":
		emit(initEvent())
		os.Stdout.Write(bytes.Repeat([]byte("x"), 16<<20+1))
		time.Sleep(10 * time.Minute)
	case "sleep":
		emit(initEvent())
		spawnGrandchild(dir, exe)
		writePID(filepath.Join(dir, "child.pid"))
		time.Sleep(10 * time.Minute)
	case "ignore_term":
		signal.Ignore(syscall.SIGTERM)
		emit(initEvent())
		writePID(filepath.Join(dir, "child.pid"))
		time.Sleep(10 * time.Minute)
	case "linger":
		emit(initEvent())
		emit(result(false, "fake answer"))
		spawnGrandchild(dir, exe)
	default:
		fail(fmt.Errorf("unknown scenario %q", scenario))
	}
}

func initEvent() map[string]any {
	return map[string]any{"type": "system", "subtype": "init", "session_id": sessionID}
}

func result(isError bool, text string) map[string]any {
	return map[string]any{"type": "result", "subtype": "success", "is_error": isError, "result": text, "session_id": sessionID}
}

func emit(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fail(err)
	}
	os.Stdout.Write(append(b, '\n'))
}

// spawnGrandchild starts a process in the same group that keeps the agent's
// stdout and stderr open, and waits until it has written its pid.
func spawnGrandchild(dir, exe string) {
	cmd := exec.Command(exe, "--fake-grandchild", dir)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		fail(err)
	}
	pidFile := filepath.Join(dir, "grandchild.pid")
	for i := 0; i < 500; i++ {
		if _, err := os.Stat(pidFile); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	fail(fmt.Errorf("grandchild did not start"))
}

// writePID writes this process's pid to path atomically.
func writePID(path string) {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		fail(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "fakeclaude:", err)
	os.Exit(97)
}
```

Run: `go build -o /dev/null ./internal/shell/testdata/fakeclaude && gofmt -l internal/shell/testdata`
Expected: builds; no gofmt output.

- [ ] **Step 2: Write the failing tests**

`internal/shell/process_test.go`:

```go
package shell

import (
	"strings"
	"testing"
)

func TestTailBuffer(t *testing.T) {
	tb := &tailBuffer{max: 8}
	for _, s := range []string{"abc", "defgh", "ijk"} {
		if n, err := tb.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", s, n, err)
		}
	}
	if got := tb.String(); got != "defghijk" {
		t.Errorf("tail = %q, want %q", got, "defghijk")
	}
	big := &tailBuffer{max: 4}
	_, _ = big.Write([]byte(strings.Repeat("x", 100) + "é"))
	if got := big.String(); got != "xxé" {
		t.Errorf("tail = %q, want %q", got, "xxé")
	}
}
```

`cmd/aits/e2e_test.go`:

```go
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
)

var (
	aitsBin string // the aits binary under test (P12)
	fakeBin string // the fake claude executable
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "aits-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	aitsBin = filepath.Join(dir, "aits")
	fakeBin = filepath.Join(dir, "fakeclaude")
	for pkg, out := range map[string]string{".": aitsBin, "../../internal/shell/testdata/fakeclaude": fakeBin} {
		build := exec.Command("go", "build", "-o", out, pkg)
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := build.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n%s", pkg, err, b)
			os.RemoveAll(dir)
			os.Exit(1)
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// vendor installs the fake claude, set to scenario, in a fresh directory and
// returns the directory, for use as PATH.
func vendor(t *testing.T, scenario string) string {
	t.Helper()
	dir := t.TempDir()
	b, err := os.ReadFile(fakeBin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "claude"), b, 0o755); err != nil {
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
	path  string   // PATH for aits; empty means an empty directory
	dir   string   // aits's working directory; empty means the test's
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

func submitArgs(extra ...string) []string {
	return append([]string{"shell", "submit", "--agent", "claude"}, extra...)
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

// decode parses stdout as exactly one JSON object with known fields (P5).
func decode(t *testing.T, stdout string) submitResult {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	dec.DisallowUnknownFields()
	var r submitResult
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("stdout holds more than one JSON value: %q", stdout)
	}
	return r
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
	bin, work := vendor(t, "ok"), t.TempDir()
	out := call{path: bin, args: submitArgs("--cwd", work, "--json", "Summarize this repo")}.run(t)
	if out.code != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", out.code, out.stdout, out.stderr)
	}
	r := decode(t, out.stdout)
	if r.Status != "ok" || r.Agent != "claude" || r.SessionID != "sess-fake-1" || r.Answer != "fake answer" ||
		r.ExitCode == nil || *r.ExitCode != 0 || r.DurationMS == nil || r.Message != "" || r.Error != nil {
		t.Fatalf("result = %+v", r)
	}
	rec := readRecord(t, bin)
	if rec.Stdin != "Summarize this repo" {
		t.Errorf("agent stdin = %q", rec.Stdin)
	}
	if want := []string{"-p", "--output-format", "stream-json", "--verbose"}; !slices.Equal(rec.Args, want) {
		t.Errorf("agent argv = %q, want %q (the prompt must not be in argv)", rec.Args, want)
	}
	wantDir, _ := filepath.EvalSymlinks(work)
	if gotDir, _ := filepath.EvalSymlinks(rec.Dir); gotDir != wantDir {
		t.Errorf("agent dir = %q, want %q", gotDir, wantDir)
	}
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
	out := call{args: submitArgs("--json", "hi")}.run(t)
	if r := decode(t, out.stdout); out.code != 1 || r.Error == nil || r.Error.Code != "agent_not_found" {
		t.Fatalf("code=%d stdout=%q", out.code, out.stdout)
	}
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

func TestSubmitEnvironment(t *testing.T) {
	bin := vendor(t, "ok")
	out := call{
		path: bin,
		env:  []string{"AITS_TEST_CANARY=secret-canary", "AITS_TEST_PASSED=passed-value"},
		args: submitArgs("--env", "AITS_TEST_PASSED", "--json", "hi"),
	}.run(t)
	if out.code != 0 {
		t.Fatalf("exit %d: %s", out.code, out.stdout)
	}
	env := readRecord(t, bin).Env
	if !slices.Contains(env, "AITS_TEST_PASSED=passed-value") {
		t.Errorf("--env variable missing from %q", env)
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "AITS_TEST_CANARY=") || strings.HasPrefix(kv, "AITS_HOME=") {
			t.Errorf("variable outside the allowlist reached the agent: %s", kv)
		}
	}
	if !slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "HOME=") }) {
		t.Errorf("allowlisted HOME missing from %q", env)
	}
}

func TestSubmitAgentFailed(t *testing.T) {
	out := call{path: vendor(t, "exit_nonzero"), args: submitArgs("--json", "hi")}.run(t)
	r := decode(t, out.stdout)
	if out.code != 1 || r.Status != "agent_failed" || r.ExitCode == nil || *r.ExitCode != 3 ||
		r.SessionID != "sess-fake-1" || r.Answer != "" || r.Message == "" {
		t.Fatalf("code=%d result=%+v", out.code, r)
	}
	if !strings.HasSuffix(r.StderrTail, "boom\n") || len(r.StderrTail) > 4096 {
		t.Errorf("stderr_tail: %d bytes ending %q", len(r.StderrTail), r.StderrTail[max(0, len(r.StderrTail)-10):])
	}
}

func TestSubmitAgentReportsError(t *testing.T) {
	out := call{path: vendor(t, "is_error"), args: submitArgs("--json", "hi")}.run(t)
	r := decode(t, out.stdout)
	if out.code != 1 || r.Status != "agent_failed" || *r.ExitCode != 0 || !strings.Contains(r.Message, "fake failure") || r.Answer != "" {
		t.Fatalf("code=%d result=%+v", out.code, r)
	}
}

func TestSubmitBadOutput(t *testing.T) {
	for _, scenario := range []string{"not_json", "no_result", "empty_result", "no_session"} {
		t.Run(scenario, func(t *testing.T) {
			out := call{path: vendor(t, scenario), args: submitArgs("--json", "hi")}.run(t)
			r := decode(t, out.stdout)
			if out.code != 1 || r.Status != "bad_agent_output" || r.Answer != "" || r.Message == "" {
				t.Fatalf("code=%d result=%+v", out.code, r)
			}
		})
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/shell/ ./cmd/aits/ 2>&1 | tail -20`
Expected: FAIL — `undefined: tailBuffer` in shell; in cmd/aits the build succeeds but tests fail with `unknown command "shell" for aits`.

- [ ] **Step 4: Write the implementation**

`internal/shell/process.go`:

```go
package shell

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// stderrTailSize is how much of the agent's stderr is kept for agent_failed.
const stderrTailSize = 4 << 10

// run starts the agent and waits for it to exit.
func run(ctx context.Context, ag agent, path string, req Request) (observation, error) {
	cmd := exec.Command(path, ag.args...)
	cmd.Dir = req.Dir
	cmd.Env = ag.environ(req.Environ, req.Pass)
	cmd.Stdin = strings.NewReader(req.Prompt)
	tail := &tailBuffer{max: stderrTailSize}
	cmd.Stderr = tail
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return observation{}, err
	}
	if err := cmd.Start(); err != nil {
		return observation{}, fmt.Errorf("start %s: %w", ag.name, err)
	}
	ev := readEvents(stdout)
	_ = cmd.Wait()
	return observation{exitCode: cmd.ProcessState.ExitCode(), events: ev, stderrTail: tail.String()}, nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

// String returns the kept bytes, dropping a rune cut at the start.
func (t *tailBuffer) String() string { return strings.ToValidUTF8(string(t.buf), "") }
```

Append to `internal/shell/shell.go` (add `"context"`, `"os/exec"` imports):

```go
// Submit validates req, runs the agent, and waits until it finishes, times
// out, or is stopped through ctx. A non-nil error means nothing was started.
func Submit(ctx context.Context, req Request) (Result, error) {
	ag, err := req.validate()
	if err != nil {
		return Result{}, err
	}
	path, err := exec.LookPath(ag.executable)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %s is not on PATH", ErrAgentNotFound, ag.executable)
	}
	start := time.Now()
	obs, err := run(ctx, ag, path, req)
	if err != nil {
		return Result{}, err
	}
	status, msg := decide(obs)
	res := Result{
		Status:     status,
		Agent:      ag.name,
		SessionID:  obs.events.sessionID,
		ExitCode:   obs.exitCode,
		DurationMS: time.Since(start).Milliseconds(),
		Message:    msg,
	}
	switch status {
	case StatusOK:
		res.Answer = obs.events.result.Result
	case StatusAgentFailed:
		res.StderrTail = obs.stderrTail
	}
	return res, nil
}
```

`internal/cli/shell.go`:

```go
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/agent-in-the-shell/aits/internal/shell"
)

func shellGroup() *Command {
	return &Command{
		Name:     "shell",
		Summary:  "Run prompts through vendor agent CLIs.",
		Children: []*Command{submitCommand()},
	}
}

// names collects the values of a repeatable flag.
type names []string

func (n *names) String() string     { return strings.Join(*n, ",") }
func (n *names) Set(v string) error { *n = append(*n, v); return nil }

func submitCommand() *Command {
	return &Command{
		Name:    "submit",
		Args:    "(PROMPT | -)",
		Summary: "Run one prompt through an agent CLI and return the answer and session id.",
		Setup: func(fs *flag.FlagSet) Runner {
			agent := fs.String("agent", "", "`AGENT` CLI to run (required). One of: "+strings.Join(shell.Agents(), ", ")+".")
			cwd := fs.String("cwd", "", "Run the agent in `DIR` (default: the current directory).")
			timeout := fs.Duration("timeout", shell.DefaultTimeout, "Stop the agent after `DURATION`, e.g. 90s or 30m.")
			var pass names
			fs.Var(&pass, "env", "Pass the variable `NAME` from this environment to the agent. Repeatable.")
			return func(ctx context.Context, env *Env, args []string) (int, error) {
				if len(args) != 1 {
					return 0, usageError("invalid_input", "expected one prompt argument, or - to read it from stdin; got %d", len(args))
				}
				prompt := args[0]
				if prompt == "-" {
					b, err := io.ReadAll(env.Stdin)
					if err != nil {
						return 0, failure("stdin_failed", "read prompt from stdin: %v", err)
					}
					prompt = string(b)
				}
				dir, err := filepath.Abs(*cwd)
				if err != nil {
					return 0, failure("internal", "resolve working directory: %v", err)
				}
				res, err := shell.Submit(ctx, shell.Request{
					Agent: *agent, Prompt: prompt, Dir: dir, Timeout: *timeout,
					Environ: env.Environ, Pass: pass,
				})
				if err != nil {
					return 0, submitError(err)
				}
				return writeResult(env, res), nil
			}
		},
	}
}

func submitError(err error) error {
	var invalid *shell.InvalidRequestError
	switch {
	case errors.As(err, &invalid):
		return usageError("invalid_input", "%s", invalid.Reason)
	case errors.Is(err, shell.ErrAgentNotFound):
		return failure("agent_not_found", "%v", err)
	default:
		return failure("agent_start_failed", "%v", err)
	}
}

// writeResult prints a run that started and returns its exit code (P5).
func writeResult(env *Env, res shell.Result) int {
	switch {
	case env.JSON:
		writeJSON(env.Stdout, res)
	case res.Status == shell.StatusOK:
		fmt.Fprintln(env.Stdout, res.Answer)
	default:
		fmt.Fprintf(env.Stderr, "aits: %s: %s\n", res.Status, res.Message)
	}
	if !env.JSON && res.SessionID != "" {
		fmt.Fprintf(env.Stderr, "session: %s\n", res.SessionID)
	}
	if res.Status == shell.StatusOK {
		return exitOK
	}
	return exitFailure
}
```

In `internal/cli/command.go`, `Root`:

```go
	root := &Command{
		Name:     "aits",
		Summary:  "Run coding agents from the command line.",
		Children: []*Command{shellGroup()},
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `gofmt -l . && go vet ./... && go test ./... 2>&1 | tail -20`
Expected: no gofmt output; `ok` for `cmd/aits`, `internal/cli`, `internal/shell`.

- [ ] **Step 6: Commit**

```bash
git add internal/shell internal/cli cmd/aits/e2e_test.go
git commit -m "feat(shell): add aits shell submit for Claude Code"
```

---

### Task 4: Process group, timeout, stop, output limit

**Files:**
- Modify: `internal/shell/process.go` (replace `run`; keep `tailBuffer`)
- Test: `cmd/aits/lifecycle_test.go`

**Interfaces:**
- Consumes: Task 3's `run` signature, `observation`, `readEvents`, `tailBuffer`, test helpers and fake scenarios `sleep`, `ignore_term`, `huge_line`, `linger`.
- Produces: unexported `killGrace`, `drainGrace`, `errTimedOut`, `supervise`, `group{pgid int; pipes []*os.File}` with `terminate()`/`settle()`.

- [ ] **Step 1: Write the failing tests**

`cmd/aits/lifecycle_test.go`:

```go
package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not appear", path)
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// assertGone fails unless pid exits within a few seconds; it kills a survivor
// so it does not outlive the test.
func assertGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Errorf("process %d is still running", pid)
}

func TestSubmitTimeout(t *testing.T) {
	bin := vendor(t, "sleep")
	out := call{path: bin, args: submitArgs("--timeout", "1s", "--json", "hi")}.run(t)
	r := decode(t, out.stdout)
	if out.code != 1 || r.Status != "timeout" || r.SessionID != "sess-fake-1" || r.Message == "" {
		t.Fatalf("code=%d result=%+v", out.code, r)
	}
	assertGone(t, readPID(t, filepath.Join(bin, "child.pid")))
	assertGone(t, readPID(t, filepath.Join(bin, "grandchild.pid")))
}

func TestSubmitTimeoutEscalatesToKill(t *testing.T) {
	bin := vendor(t, "ignore_term")
	start := time.Now()
	out := call{path: bin, args: submitArgs("--timeout", "1s", "--json", "hi")}.run(t)
	if r := decode(t, out.stdout); out.code != 1 || r.Status != "timeout" {
		t.Fatalf("code=%d result=%+v", out.code, r)
	}
	if elapsed := time.Since(start); elapsed < 5*time.Second {
		t.Errorf("finished after %s; SIGKILL must wait for the 5s grace period", elapsed)
	}
	assertGone(t, readPID(t, filepath.Join(bin, "child.pid")))
}

func TestSubmitStopped(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			bin := vendor(t, "sleep")
			cmd, stdout, stderr := call{path: bin, args: submitArgs("--json", "hi")}.command(t)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitFile(t, filepath.Join(bin, "child.pid"))
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			out := finish(t, cmd.Wait(), stdout, stderr)
			r := decode(t, out.stdout)
			if out.code != 1 || r.Status != "stopped" || r.SessionID != "sess-fake-1" {
				t.Fatalf("code=%d result=%+v", out.code, r)
			}
			assertGone(t, readPID(t, filepath.Join(bin, "child.pid")))
			assertGone(t, readPID(t, filepath.Join(bin, "grandchild.pid")))
		})
	}
}

func TestSubmitOutputTooLarge(t *testing.T) {
	start := time.Now()
	out := call{path: vendor(t, "huge_line"), args: submitArgs("--json", "hi")}.run(t)
	r := decode(t, out.stdout)
	if out.code != 1 || r.Status != "output_too_large" || r.SessionID != "sess-fake-1" || r.Answer != "" {
		t.Fatalf("code=%d result=%+v", out.code, r)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("took %s; the agent must be terminated as soon as the limit is hit", elapsed)
	}
}

func TestSubmitLingeringGrandchild(t *testing.T) {
	bin := vendor(t, "linger")
	start := time.Now()
	out := call{path: bin, args: submitArgs("--json", "hi")}.run(t)
	if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" {
		t.Fatalf("code=%d result=%+v", out.code, r)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %s; a grandchild holding stdout must not keep aits waiting", elapsed)
	}
	assertGone(t, readPID(t, filepath.Join(bin, "grandchild.pid")))
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/aits/ -run 'TestSubmit(Timeout|Stopped|OutputTooLarge|LingeringGrandchild)' -timeout 90s 2>&1 | tail -20`
Expected: FAIL — `panic: test timed out after 1m30s` (the current `run` has no timeout or group handling and waits forever on the sleeping fake).

The panic leaves the sleeping fakes and their `aits` parents running; clean them up:

Run: `pkill -f 'aits-test-[^/]*/aits' ; pkill -f '/TestSubmit[^/]*/[0-9]+/claude' ; pkill -f -- '--fake-grandchild' ; true`
Expected: no output.

- [ ] **Step 3: Write the implementation**

Replace `run` in `internal/shell/process.go` (keep `stderrTailSize` and `tailBuffer`); the imports become:

```go
import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)
```

```go
const (
	// killGrace is the time between SIGTERM and SIGKILL to the group (P10).
	killGrace = 5 * time.Second
	// drainGrace is how long reads wait for the pipes to close after SIGKILL.
	drainGrace = time.Second
)

var errTimedOut = errors.New("agent timed out")

// run starts the agent in its own process group and supervises it until it
// has exited and its output pipes are closed.
func run(ctx context.Context, ag agent, path string, req Request) (observation, error) {
	outR, outW, err := os.Pipe()
	if err != nil {
		return observation{}, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return observation{}, err
	}
	defer outR.Close()
	defer errR.Close()
	cmd := exec.Command(path, ag.args...)
	cmd.Dir = req.Dir
	cmd.Env = ag.environ(req.Environ, req.Pass)
	cmd.Stdin = strings.NewReader(req.Prompt)
	cmd.Stdout, cmd.Stderr = outW, errW
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = cmd.Start()
	outW.Close()
	errW.Close()
	if err != nil {
		return observation{}, fmt.Errorf("start %s: %w", ag.name, err)
	}
	return supervise(ctx, cmd, outR, errR, req.Timeout), nil
}

// supervise waits for three things: the agent to exit, its stdout to reach
// EOF, and its stderr to reach EOF. A timeout, a cancelled ctx, an oversized
// event, or the agent's own exit terminates the whole group, so nothing the
// agent started outlives it (P10).
func supervise(ctx context.Context, cmd *exec.Cmd, out, errOut *os.File, timeout time.Duration) observation {
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, errTimedOut)
	defer cancel()
	g := &group{pgid: cmd.Process.Pid, pipes: []*os.File{out, errOut}}

	evCh := make(chan events, 1)
	go func() { evCh <- readEvents(out) }()
	tail := &tailBuffer{max: stderrTailSize}
	tailCh := make(chan struct{}, 1)
	go func() { _, _ = io.Copy(tail, errOut); tailCh <- struct{}{} }()
	waitCh := make(chan struct{}, 1)
	go func() { _ = cmd.Wait(); waitCh <- struct{}{} }()

	var o observation
	done := ctx.Done()
	for pending := 3; pending > 0; {
		select {
		case <-waitCh:
			pending--
			o.exitCode = cmd.ProcessState.ExitCode()
			g.terminate()
		case o.events = <-evCh:
			pending--
			if o.events.tooLarge {
				g.terminate()
			}
		case <-tailCh:
			pending--
		case <-done:
			done = nil
			if errors.Is(context.Cause(ctx), errTimedOut) {
				o.timedOut = true
			} else {
				o.stopped = true
			}
			g.terminate()
		}
	}
	g.settle()
	o.stderrTail = tail.String()
	return o
}

// group is the agent's process group (P10).
type group struct {
	pgid    int
	pipes   []*os.File
	once    sync.Once
	started time.Time
}

// terminate sends SIGTERM to the group, SIGKILL killGrace later, and makes
// reads from the pipes give up shortly after that, so that a process that left
// the group cannot hold aits open.
func (g *group) terminate() {
	g.once.Do(func() {
		g.started = time.Now()
		_ = syscall.Kill(-g.pgid, syscall.SIGTERM)
		time.AfterFunc(killGrace, func() { _ = syscall.Kill(-g.pgid, syscall.SIGKILL) })
		for _, p := range g.pipes {
			_ = p.SetReadDeadline(g.started.Add(killGrace + drainGrace))
		}
	})
}

// settle waits, until the drain deadline at most, for the group to be empty.
func (g *group) settle() {
	if g.started.IsZero() {
		return
	}
	deadline := g.started.Add(killGrace + drainGrace)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(-g.pgid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `gofmt -l . && go vet ./... && go test ./... 2>&1 | tail -20`
Expected: no gofmt output; all packages `ok` (`cmd/aits` takes about 10s because of the kill-grace test).

- [ ] **Step 5: Run the lifecycle tests with the race detector**

Run: `CGO_ENABLED=1 go test -race ./internal/shell/ ./cmd/aits/ -run 'TestSubmit' 2>&1 | tail -5`
Expected: `ok` for both, no `DATA RACE`. (The race detector needs cgo; this is a local check only, the shipped build stays `CGO_ENABLED=0`.)

- [ ] **Step 6: Commit**

```bash
git add internal/shell/process.go cmd/aits/lifecycle_test.go
git commit -m "feat(shell): run the agent in its own process group with timeout, stop and output limit"
```

---

### Task 5: Guards — P3 dependencies, P4 grammar, P5 output channel, P13 CLI reference

**Files:**
- Create: `internal/archtest/doc.go`, `internal/archtest/deps_test.go`, `cmd/aits/grammar_test.go`, `cmd/aits/output_test.go`, `cmd/aits/docs_test.go`, `docs/cli.md` (generated)

**Interfaces:**
- Consumes: `cli.Root()`, `cli.Command`, Task 3 test helpers (`call`, `vendor`, `decode`, `readRecord`, `submitArgs`).
- Produces: `walk(c *cli.Command, path []string, fn func([]string, *cli.Command))` in `cmd/aits` tests.

- [ ] **Step 1: Write the P3 guard and watch its self-test fail**

`internal/archtest/doc.go`:

```go
// Package archtest holds repository-wide structural tests that enforce the
// principles in docs/principles.md.
package archtest
```

`internal/archtest/deps_test.go`:

```go
package archtest

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// directRequires returns the modules go.mod requires directly.
func directRequires(gomod string) []string {
	var mods []string
	inBlock := false
	for _, line := range strings.Split(gomod, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "require (":
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		case strings.HasPrefix(line, "require "):
			line = strings.TrimPrefix(line, "require ")
		case !inBlock:
			continue
		}
		if strings.Contains(line, "// indirect") {
			continue
		}
		if f := strings.Fields(line); len(f) >= 2 {
			mods = append(mods, f[0])
		}
	}
	slices.Sort(mods)
	return mods
}

// listedModules returns the first column of the table in docs/dependencies.md.
func listedModules(md string) []string {
	var mods []string
	for _, line := range strings.Split(md, "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cell := strings.Trim(strings.TrimSpace(strings.Split(line, "|")[1]), "`")
		if cell == "Module" || strings.HasPrefix(cell, "---") || cell == "" {
			continue
		}
		mods = append(mods, cell)
	}
	slices.Sort(mods)
	return mods
}

func TestParsers(t *testing.T) {
	gomod := "module x\n\ngo 1.26\n\nrequire a.example/one v1.0.0\n\nrequire (\n\tb.example/two v0.1.0\n\tc.example/three v1.2.0 // indirect\n)\n"
	if got := directRequires(gomod); !slices.Equal(got, []string{"a.example/one", "b.example/two"}) {
		t.Errorf("directRequires = %q", got)
	}
	md := "| Module | Used by | Why | Added in |\n| --- | --- | --- | --- |\n| `b.example/two` | shell | reason | #9 |\n"
	if got := listedModules(md); !slices.Equal(got, []string{"b.example/two"}) {
		t.Errorf("listedModules = %q", got)
	}
}

// TestDependenciesAreJustified enforces P3: every direct dependency is listed,
// with its reason, in docs/dependencies.md, and the list has no stale rows.
func TestDependenciesAreJustified(t *testing.T) {
	gomod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	md, err := os.ReadFile("../../docs/dependencies.md")
	if err != nil {
		t.Fatal(err)
	}
	required, listed := directRequires(string(gomod)), listedModules(string(md))
	for _, m := range required {
		if !slices.Contains(listed, m) {
			t.Errorf("go.mod requires %s but docs/dependencies.md does not justify it (P3)", m)
		}
	}
	for _, m := range listed {
		if !slices.Contains(required, m) {
			t.Errorf("docs/dependencies.md lists %s but go.mod does not require it", m)
		}
	}
}
```

To watch the guard fail, temporarily add a requirement:

Run: `printf '\nrequire example.com/unjustified v1.0.0\n' >> go.mod && go test ./internal/archtest/ -run TestDependenciesAreJustified 2>&1 | tail -3; git checkout go.mod`
Expected: `FAIL` with `go.mod requires example.com/unjustified but docs/dependencies.md does not justify it (P3)`; `go.mod` restored.

Run: `go test ./internal/archtest/`
Expected: `ok`.

- [ ] **Step 2: Write the P4, P5 and P13 guards**

`cmd/aits/grammar_test.go`:

```go
package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/agent-in-the-shell/aits/internal/cli"
)

// walk calls fn for c and every command below it; path excludes "aits".
func walk(c *cli.Command, path []string, fn func(path []string, c *cli.Command)) {
	fn(path, c)
	for _, ch := range c.Children {
		walk(ch, append(slices.Clone(path), ch.Name), fn)
	}
}

// TestGrammarUnknownFlag enforces P4 on every command: an unknown flag is
// exit 2, never ignored.
func TestGrammarUnknownFlag(t *testing.T) {
	walk(cli.Root(), nil, func(path []string, _ *cli.Command) {
		args := append(slices.Clone(path), "--no-such-flag", "--json")
		out := call{args: args}.run(t)
		if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "unknown_flag" {
			t.Errorf("aits %s: code=%d stdout=%q", strings.Join(args, " "), out.code, out.stdout)
		}
	})
}

// TestGrammarUnknownCommand enforces P4 on every group: an unknown command
// is exit 2, never guessed and never sent to an agent.
func TestGrammarUnknownCommand(t *testing.T) {
	walk(cli.Root(), nil, func(path []string, c *cli.Command) {
		if c.Setup != nil {
			return
		}
		args := append(slices.Clone(path), "no-such-command", "--json")
		out := call{args: args}.run(t)
		if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "unknown_command" {
			t.Errorf("aits %s: code=%d stdout=%q", strings.Join(args, " "), out.code, out.stdout)
		}
	})
}

func TestGrammarHelp(t *testing.T) {
	walk(cli.Root(), nil, func(path []string, _ *cli.Command) {
		out := call{args: append(slices.Clone(path), "--help")}.run(t)
		if out.code != 0 || !strings.HasPrefix(out.stdout, "Usage: aits") || out.stderr != "" {
			t.Errorf("aits %s --help: code=%d stdout=%q stderr=%q", strings.Join(path, " "), out.code, out.stdout, out.stderr)
		}
	})
}

func TestGrammarFlagsAnywhere(t *testing.T) {
	orders := [][]string{
		{"shell", "submit", "--agent", "claude", "--json", "hello"},
		{"shell", "submit", "hello", "--agent", "claude", "--json"},
		{"--json", "shell", "submit", "--agent=claude", "hello"},
	}
	for _, args := range orders {
		bin := vendor(t, "ok")
		out := call{path: bin, args: args}.run(t)
		if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" {
			t.Errorf("aits %s: code=%d stdout=%q", strings.Join(args, " "), out.code, out.stdout)
			continue
		}
		if got := readRecord(t, bin).Stdin; got != "hello" {
			t.Errorf("aits %s: prompt %q", strings.Join(args, " "), got)
		}
	}
}

func TestGrammarDoubleDash(t *testing.T) {
	bin := vendor(t, "ok")
	out := call{path: bin, args: submitArgs("--json", "--", "--not-a-flag")}.run(t)
	if r := decode(t, out.stdout); out.code != 0 || r.Status != "ok" || readRecord(t, bin).Stdin != "--not-a-flag" {
		t.Errorf("code=%d stdout=%q", out.code, out.stdout)
	}
	bin = vendor(t, "ok")
	out = call{path: bin, args: submitArgs("--", "--json")}.run(t)
	if out.code != 0 || out.stdout != "fake answer\n" || readRecord(t, bin).Stdin != "--json" {
		t.Errorf("--json after -- must be the prompt: code=%d stdout=%q", out.code, out.stdout)
	}
}

func TestGrammarLeafFlagBeforeCommand(t *testing.T) {
	out := call{args: []string{"shell", "--agent", "claude", "submit", "hi", "--json"}}.run(t)
	if r := decode(t, out.stdout); out.code != 2 || r.Error == nil || r.Error.Code != "unknown_flag" {
		t.Errorf("code=%d stdout=%q", out.code, out.stdout)
	}
}
```

`cmd/aits/output_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

// TestOutputChannels enforces P5 for every kind of outcome: with --json,
// stdout is exactly one JSON object (an error envelope or a result) and stderr
// is empty; without it, stdout carries only a successful answer.
func TestOutputChannels(t *testing.T) {
	tests := []struct {
		name     string
		scenario string // "" means no agent on PATH
		args     []string
		code     int
	}{
		{"unknown flag", "ok", submitArgs("--bogus", "hi"), 2},
		{"unknown command", "ok", []string{"nope"}, 2},
		{"invalid input", "ok", []string{"shell", "submit", "hi"}, 2},
		{"agent not found", "", submitArgs("hi"), 1},
		{"agent failed", "exit_nonzero", submitArgs("hi"), 1},
		{"bad output", "not_json", submitArgs("hi"), 1},
		{"ok", "ok", submitArgs("hi"), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
```

`cmd/aits/docs_test.go`:

```go
package main

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/agent-in-the-shell/aits/internal/cli"
)

var update = flag.Bool("update", false, "rewrite docs/cli.md from the binary's help output")

// TestCLIDocs enforces P13: docs/cli.md is exactly the help the binary prints.
func TestCLIDocs(t *testing.T) {
	var b strings.Builder
	b.WriteString("# CLI reference\n\nGenerated from the help output of `aits` by\n" +
		"`go test ./cmd/aits -run TestCLIDocs -update`. Do not edit by hand.\n")
	walk(cli.Root(), nil, func(path []string, _ *cli.Command) {
		out := call{args: append(slices.Clone(path), "--help")}.run(t)
		if out.code != 0 {
			t.Fatalf("aits %s --help: exit %d", strings.Join(path, " "), out.code)
		}
		fmt.Fprintf(&b, "\n## %s\n\n```text\n%s```\n", strings.Join(append([]string{"aits"}, path...), " "), out.stdout)
	})
	const file = "../../docs/cli.md"
	if *update {
		if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v; run: go test ./cmd/aits -run TestCLIDocs -update", err)
	}
	if string(want) != b.String() {
		t.Fatalf("docs/cli.md is out of date; run: go test ./cmd/aits -run TestCLIDocs -update")
	}
}
```

- [ ] **Step 3: Run the guards and watch the docs guard fail**

Run: `go test ./cmd/aits/ -run 'TestGrammar|TestOutput|TestCLIDocs' 2>&1 | tail -8`
Expected: grammar and output tests pass (they pin behaviour Tasks 1–4 built); `TestCLIDocs` FAILS with `no such file or directory; run: go test ./cmd/aits -run TestCLIDocs -update`.

- [ ] **Step 4: Prove the P4 and P5 guards can fail**

Temporarily make the dispatcher ignore unknown flags: in `internal/cli/parse.go` change `return nil, usageError("unknown_flag", "unknown flag %s", a)` to `continue`.

Run: `go test ./cmd/aits/ -run 'TestGrammarUnknownFlag|TestOutputChannels' 2>&1 | grep -c -- '--- FAIL'`
Expected: a count of at least 2. Then restore: `git checkout internal/cli/parse.go`.

- [ ] **Step 5: Generate the CLI reference and run everything**

Run: `go test ./cmd/aits -run TestCLIDocs -update && cat docs/cli.md && gofmt -l . && go vet ./... && go test ./...`
Expected: `docs/cli.md` shows sections `aits`, `aits shell`, `aits shell submit`, `aits help`; all packages `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/archtest cmd/aits/grammar_test.go cmd/aits/output_test.go cmd/aits/docs_test.go docs/cli.md
git commit -m "test: guard dependencies, grammar, output channels and CLI reference"
```

---

### Task 6: CI, live test, docs, pull request

**Files:**
- Create: `cmd/aits/live_test.go`
- Modify: `.github/workflows/pr.yml`, `README.md`, `CONTRIBUTING.md` (Tests section), `docs/principles.md` (P12)

**Interfaces:**
- Consumes: `aitsBin`, `finish`, `decode` from the `cmd/aits` tests.

- [ ] **Step 1: Write the live test**

`cmd/aits/live_test.go`:

```go
//go:build live

package main

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
)

// TestLiveClaude runs the real Claude Code CLI (P12: opt-in, never in CI). It
// needs a logged-in claude on PATH and the real HOME, where its credentials
// live; AITS_HOME stays temporary. It spends one model call.
//
//	go test -tags live ./cmd/aits -run TestLive
func TestLiveClaude(t *testing.T) {
	cmd := exec.Command(aitsBin, "shell", "submit", "--agent", "claude", "--cwd", t.TempDir(),
		"--timeout", "5m", "--json", "Reply with exactly: OK")
	cmd.Env = append(os.Environ(), "AITS_HOME="+t.TempDir())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	out := finish(t, cmd.Run(), &stdout, &stderr)
	r := decode(t, out.stdout)
	if out.code != 0 || r.Status != "ok" || r.SessionID == "" || r.Answer == "" {
		t.Fatalf("code=%d result=%+v stderr=%q", out.code, r, out.stderr)
	}
}
```

Run: `go vet -tags live ./cmd/aits && go test ./cmd/aits -run TestLive -v 2>&1 | tail -3`
Expected: vet clean; without the tag `testing: warning: no tests to run` and `ok`.

Run: `go test -tags live ./cmd/aits -run TestLive -v 2>&1 | tail -5`
Expected: `--- PASS: TestLiveClaude` (uses the developer's logged-in `claude`; one model call).

- [ ] **Step 2: Add the CI test job**

Append to `.github/workflows/pr.yml` under `jobs:`:

```yaml
  test:
    strategy:
      fail-fast: false
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    env:
      CGO_ENABLED: "0"
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: false
      - name: Read-only HOME (P12)
        run: |
          ro="$RUNNER_TEMP/read-only-home"
          mkdir -p "$ro" && chmod 0555 "$ro"
          {
            echo "GOCACHE=$RUNNER_TEMP/gocache"
            echo "GOMODCACHE=$RUNNER_TEMP/gomodcache"
            echo "GOPATH=$RUNNER_TEMP/gopath"
            echo "HOME=$ro"
          } >> "$GITHUB_ENV"
      - name: gofmt
        run: test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
      - name: go vet
        run: go vet ./...
      - name: go test
        run: go test ./...
```

Run: `ruby -e 'require "yaml"; y = YAML.load_file(".github/workflows/pr.yml"); puts y["jobs"].keys.inspect'`
Expected: `["links-issue", "test"]`.

- [ ] **Step 3: Update the docs**

`docs/principles.md`, P12 **Rule**, replace the sentence
"Tests against real vendors are opt-in behind a build tag and are not part of default CI." with:

```markdown
Tests against real vendors are opt-in behind a build tag and are not part of
default CI; they use the real `HOME`, where vendor credentials live, and keep
`AITS_HOME` temporary.
```

`README.md`: replace the status note and add, after it, a usage section:

```markdown
> **Status:** pre-release. `aits shell submit` works with Claude Code; the
> other command groups are planned.

## Install

    go install github.com/agent-in-the-shell/aits/cmd/aits@latest

## Usage

    aits shell submit --agent claude --cwd ~/src/proj --json "Summarize what this repo does"

prints

    {"status":"ok","agent":"claude","session_id":"…","answer":"…","exit_code":0,"duration_ms":12345}

The full reference is [docs/cli.md](docs/cli.md).
```

`CONTRIBUTING.md`, `## Tests`: replace "There is no code yet. From the first feature onward:" with:

```markdown
Run `go test ./...`. CI runs it on Linux and macOS with `HOME` read-only, plus
`gofmt -l .` and `go vet ./...`.
```

and append to the list:

```markdown
- The CLI reference is generated from help: after changing a command, run
  `go test ./cmd/aits -run TestCLIDocs -update` (P13).
- `go test -tags live ./cmd/aits -run TestLive` runs one real Claude Code call
  with your own login.
```

Run: `gofmt -l . && go vet ./... && go test ./...`
Expected: all `ok`.

- [ ] **Step 4: Commit**

```bash
git add cmd/aits/live_test.go .github/workflows/pr.yml README.md CONTRIBUTING.md docs/principles.md
git commit -m "ci: run tests on Linux and macOS with read-only HOME; add live test and docs"
```

- [ ] **Step 5: Open the pull request and wait for CI**

```bash
git push -u origin feat/shell-submit
gh pr create --title "aits shell submit: run one prompt through Claude Code" --body-file <(cat <<'EOF'
Closes #2

## What changed
…(summary of Tasks 1–6)…

## Verification
…(commands run and their actual results)…

## Principles
P3, P4, P5, P6, P8, P9, P10, P12, P13

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01B6kuFwnH7PWEUpuFq1zj3y
EOF
)
gh pr checks --watch
```

Expected: `links-issue`, `test (ubuntu-latest)`, `test (macos-latest)` all pass. A failure is debugged (superpowers:systematic-debugging), fixed in a new commit, and pushed.

- [ ] **Step 6: Require the test job on main**

```bash
gh api -X PATCH repos/agent-in-the-shell/aits/branches/main/protection/required_status_checks \
  -F strict=false -f 'contexts[]=links-issue' -f 'contexts[]=test (ubuntu-latest)' -f 'contexts[]=test (macos-latest)'
gh api repos/agent-in-the-shell/aits/branches/main/protection/required_status_checks --jq .contexts
```

Expected: `["links-issue","test (ubuntu-latest)","test (macos-latest)"]`.
