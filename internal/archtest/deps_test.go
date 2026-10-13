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
