package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestVersionRecorded checks the premise of the "version" rows of
// TestOutputChannels: the test build, made by "go build" in a git checkout,
// records a module version, not "(devel)" (#102).
func TestVersionRecorded(t *testing.T) {
	if !strings.HasPrefix(aitsVersion, "v") {
		t.Fatalf("the test build records version %q; build it from a git checkout", aitsVersion)
	}
}

// TestVersionUnknown: a binary built without version control information
// records "(devel)", which aits refuses to print as a version (P6, #102).
func TestVersionUnknown(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "aits")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", bin, ".")
	build.Env = append(build.Environ(), "CGO_ENABLED=0")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	const msg = "this aits records no version: it was built without version control information (go build -buildvcs=false, or outside a git checkout)"
	for _, json := range []bool{true, false} {
		args := []string{"version"}
		if json {
			args = append(args, "--json")
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = []string{"HOME=" + t.TempDir(), "AITS_HOME=" + t.TempDir()}
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := -1
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		}
		if json {
			e := decode(t, stdout.String()).Error
			if code != 1 || stderr.String() != "" || e == nil || e.Code != "unknown_version" || e.Message != msg {
				t.Errorf("--json: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		} else if code != 1 || stdout.String() != "" || stderr.String() != "aits: "+msg+"\n" {
			t.Errorf("text: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	}
	if got := helpErrors(t, []string{"version"})["unknown_version"]; got != "exit 1" {
		t.Errorf("aits version --help lists unknown_version with %q, want exit 1", got)
	}
}
