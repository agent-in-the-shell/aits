package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestVersionUnknown: a binary built without version control information
// records "(devel)", which aits refuses to print as a version (P6, #102).
func TestVersionUnknown(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "aits")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", bin, ".")
	build.Env = append(build.Environ(), "CGO_ENABLED=0")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	out := call{bin: bin, args: []string{"version", "--json"}}.run(t)
	e := decode(t, out.stdout).Error
	if out.code != 1 || out.stderr != "" || e == nil || e.Code != "unknown_version" {
		t.Fatalf("--json: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
	if out := (call{bin: bin, args: []string{"version"}}).run(t); out.code != 1 || out.stdout != "" || out.stderr != "aits: "+e.Message+"\n" {
		t.Errorf("text: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
}
