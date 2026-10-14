// Command fakevendor stands in for the vendor agent CLIs in tests (P12).
//
// It speaks the dialect named by its executable's base name, "claude" or
// "codex". It reads a scenario name from the file "scenario" next to its
// executable, records what it was given to "record.json" in the same
// directory, and then behaves as the scenario says. The scenario comes from a
// file, not the environment, because aits filters the environment it passes
// on (P8).
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

// dialect writes one vendor's events.
type dialect struct {
	start     func()            // the events that open a run, carrying the session id
	progress  func()            // an event that is neither the session id nor the answer
	answer    func(text string) // a reply and the end of the run
	fail      func(text string) // a reported error and the end of the run
	noSession func(text string) // a reply and the end of the run, without any session id
}

var dialects = map[string]dialect{
	"claude": {
		start: func() { emit(map[string]any{"type": "system", "subtype": "init", "session_id": sessionID}) },
		progress: func() {
			emit(map[string]any{"type": "assistant", "session_id": sessionID, "message": map[string]any{"content": []any{}}})
		},
		answer:    func(text string) { emit(claudeResult(false, text, sessionID)) },
		fail:      func(text string) { emit(claudeResult(true, text, sessionID)) },
		noSession: func(text string) { emit(claudeResult(false, text, "")) },
	},
	"codex": {
		start: func() {
			emit(map[string]any{"type": "thread.started", "thread_id": sessionID})
			emit(map[string]any{"type": "turn.started"})
		},
		progress: func() {
			emit(codexItem(map[string]any{"type": "command_execution", "command": "ls", "aggregated_output": "README.md\n", "exit_code": 0}))
		},
		answer: codexAnswer,
		fail: func(text string) {
			emit(map[string]any{"type": "error", "message": "Reconnecting... 1/5"})
			emit(map[string]any{"type": "turn.failed", "error": map[string]any{"message": text}})
		},
		noSession: func(text string) {
			emit(map[string]any{"type": "turn.started"})
			codexAnswer(text)
		},
	},
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
	d, ok := dialects[filepath.Base(exe)]
	if !ok {
		fail(fmt.Errorf("no dialect for executable name %q", filepath.Base(exe)))
	}
	dir := filepath.Dir(exe)
	scenario, err := os.ReadFile(filepath.Join(dir, "scenario"))
	if err != nil {
		fail(err)
	}
	name := strings.TrimSpace(string(scenario))
	var stdin []byte
	if name != "ignore_stdin" {
		if stdin, err = io.ReadAll(os.Stdin); err != nil {
			fail(err)
		}
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
	play(d, name, dir, exe)
}

func play(d dialect, scenario, dir, exe string) {
	switch scenario {
	case "ok":
		d.start()
		d.progress()
		d.answer("fake answer")
	case "exit_nonzero":
		d.start()
		os.Stderr.Write(bytes.Repeat([]byte("x"), 100<<10))
		os.Stderr.WriteString("\nboom\n")
		os.Exit(3)
	case "agent_error":
		// What both CLIs do when not logged in: report the error, exit 1.
		d.start()
		d.fail("fake failure\nsecond line")
		os.Exit(1)
	case "agent_error_exit0":
		d.start()
		d.fail("fake failure\nsecond line")
	case "not_json":
		fmt.Println("this is not json")
		d.start()
		d.answer("fake answer")
	case "no_result":
		d.start()
	case "empty_result":
		d.start()
		d.answer("")
	case "no_session":
		d.noSession("fake answer")
	case "huge_line":
		d.start()
		os.Stdout.Write(bytes.Repeat([]byte("x"), 16<<20+1))
		time.Sleep(10 * time.Minute)
	case "sleep":
		d.start()
		spawnGrandchild(dir, exe)
		writePID(filepath.Join(dir, "child.pid"))
		time.Sleep(10 * time.Minute)
	case "ignore_term":
		signal.Ignore(syscall.SIGTERM)
		d.start()
		writePID(filepath.Join(dir, "child.pid"))
		time.Sleep(10 * time.Minute)
	case "ignore_stdin", "linger":
		// A grandchild keeps stdout open, and stdin too: for ignore_stdin the
		// prompt was never read (see main).
		d.start()
		d.answer("fake answer")
		spawnGrandchild(dir, exe)
	case "several_messages": // Codex only
		d.start()
		emit(codexItem(map[string]any{"type": "agent_message", "text": "Let me look."}))
		d.progress()
		d.answer("fake answer")
	case "retry_errors": // Codex only
		d.start()
		emit(map[string]any{"type": "error", "message": "Reconnecting... 1/5"})
		emit(map[string]any{"type": "error", "message": "Reconnecting... 2/5"})
		d.answer("fake answer")
	case "not_git": // Codex only
		os.Stderr.WriteString("Not inside a trusted directory and --skip-git-repo-check was not specified.\n")
		os.Exit(1)
	default:
		fail(fmt.Errorf("unknown scenario %q", scenario))
	}
}

func claudeResult(isError bool, text, session string) map[string]any {
	ev := map[string]any{"type": "result", "subtype": "success", "is_error": isError, "result": text}
	if session != "" {
		ev["session_id"] = session
	}
	return ev
}

func codexItem(item map[string]any) map[string]any {
	return map[string]any{"type": "item.completed", "item": item}
}

func codexAnswer(text string) {
	emit(codexItem(map[string]any{"type": "agent_message", "text": text}))
	emit(map[string]any{"type": "turn.completed", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
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
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
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
	fmt.Fprintln(os.Stderr, "fakevendor:", err)
	os.Exit(97)
}
