package archtest

import (
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// directRequires returns the modules go.mod requires directly, as the go
// command reads them.
func directRequires(t *testing.T, gomod string) []string {
	t.Helper()
	out, err := exec.Command("go", "mod", "edit", "-json", gomod).Output()
	if err != nil {
		t.Fatalf("go mod edit -json: %v", err)
	}
	var mod struct {
		Require []struct {
			Path     string
			Indirect bool
		}
	}
	if err := json.Unmarshal(out, &mod); err != nil {
		t.Fatal(err)
	}
	var mods []string
	for _, r := range mod.Require {
		if !r.Indirect {
			mods = append(mods, r.Path)
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

func TestListedModules(t *testing.T) {
	md := "| Module | Used by | Why | Added in |\n| --- | --- | --- | --- |\n| `b.example/two` | shell | reason | #9 |\n"
	if got := listedModules(md); !slices.Equal(got, []string{"b.example/two"}) {
		t.Errorf("listedModules = %q", got)
	}
}

// TestDependenciesAreJustified enforces P3: every direct dependency is listed,
// with its reason, in docs/dependencies.md, and the list has no stale rows.
func TestDependenciesAreJustified(t *testing.T) {
	md, err := os.ReadFile("../../docs/dependencies.md")
	if err != nil {
		t.Fatal(err)
	}
	required, listed := directRequires(t, "../../go.mod"), listedModules(string(md))
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
