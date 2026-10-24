package archtest

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// detaching matches the calls that would let aits re-execute itself or start
// a process outside its own session: the ways to daemonize. Children are
// started with os/exec in their own process group (Setpgid), which is not
// matched.
var detaching = regexp.MustCompile(`\bos\.(Executable|StartProcess)\b|\bsyscall\.(ForkExec|Exec)\b|\b(Setsid|Noctty)\b`)

// TestNoDaemonize enforces P10: aits never forks into the background, detaches
// from its session or re-executes itself (#111). It reads the sources the go
// tool builds, as TestOnlyStoreWrites does; tests and testdata may use these
// calls (a fake vendor finds itself with os.Executable).
func TestNoDaemonize(t *testing.T) {
	scanned := 0
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != "../.." && ignored(d.Name()) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for _, loc := range detaching.FindAllIndex(src, -1) {
			line := bytes.Count(src[:loc[0]], []byte("\n")) + 1
			t.Errorf("%s:%d detaches or re-executes aits: %s", path, line, src[loc[0]:loc[1]])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned == 0 {
		t.Fatal("no source file was scanned")
	}
}

// TestDetachingPattern pins detaching: each call, a field set in a
// SysProcAttr, a function taken as a value; not a process group or
// look-alike names.
func TestDetachingPattern(t *testing.T) {
	caught := []string{
		"exe, err := os.Executable()",
		"os.StartProcess(exe, argv, attr)",
		"syscall.ForkExec(exe, argv, attr)",
		"syscall.Exec(exe, argv, env)",
		"&syscall.SysProcAttr{Setsid: true}",
		"attr.Noctty = true",
		"start := os.StartProcess",
	}
	for _, src := range caught {
		if !detaching.MatchString(src) {
			t.Errorf("%s is not caught", src)
		}
	}
	allowed := []string{
		"&syscall.SysProcAttr{Setpgid: true}",
		"exec.Command(name, args...)",
		"exec.LookPath(name)",
		"syscall.Kill(-pgid, syscall.SIGTERM)",
		"syscall.Exit(1)",
		"os.Getpid()",
		"shell.Executable(name)",
	}
	for _, src := range allowed {
		if detaching.MatchString(src) {
			t.Errorf("%s is caught", src)
		}
	}
}
