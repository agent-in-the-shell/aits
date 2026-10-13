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
	play(name, dir, exe)
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
	case "ignore_stdin":
		// Never reads the prompt; a grandchild keeps stdin (and stdout) open.
		emit(initEvent())
		emit(result(false, "fake answer"))
		spawnGrandchild(dir, exe)
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
	fmt.Fprintln(os.Stderr, "fakeclaude:", err)
	os.Exit(97)
}
