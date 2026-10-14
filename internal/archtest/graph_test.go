package archtest

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/agent-in-the-shell/aits/internal/"

// allowed is the declared dependency graph between command groups (P1): the
// groups each group may import. Shared packages (cli, store, archtest) are
// not groups.
var allowed = map[string][]string{
	"shell":   nil,
	"session": nil,
	"task":    {"shell"},
}

// TestGroupDependencies enforces P1: every import between command groups is
// declared above, and the declared graph has no cycle.
func TestGroupDependencies(t *testing.T) {
	cmd := exec.Command("go", "list", "-f", `{{.ImportPath}} {{join .Imports " "}}`, "./internal/...")
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		from := group(fields[0])
		if from == "" {
			continue
		}
		if _, ok := allowed[from]; !ok {
			t.Errorf("internal/%s is not a declared group; add it to allowed (P1)", from)
			continue
		}
		for _, imp := range fields[1:] {
			if to := group(imp); to != "" && to != from && !slices.Contains(allowed[from], to) {
				t.Errorf("%s imports %s, an edge not declared in allowed (P1)", from, to)
			}
		}
	}
	var visit func(g string, path []string)
	visit = func(g string, path []string) {
		if slices.Contains(path, g) {
			t.Fatalf("declared groups form a cycle: %s", strings.Join(append(path, g), " → "))
		}
		for _, next := range allowed[g] {
			visit(next, append(path, g))
		}
	}
	for g := range allowed {
		visit(g, nil)
	}
}

// group returns the command group an internal package belongs to, or "" for
// shared packages and anything outside internal/.
func group(pkg string) string {
	rest, ok := strings.CutPrefix(pkg, module)
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "/")
	switch name {
	case "cli", "store", "archtest":
		return ""
	}
	return name
}
