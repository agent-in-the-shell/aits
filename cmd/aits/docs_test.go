package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/agent-in-the-shell/aits/internal/cli"
	"github.com/agent-in-the-shell/aits/internal/shell"
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

// TestSubmitHelpDocumentsOutput enforces the P5 rule that output shapes are
// documented in help: every result field, status and error code the code can
// produce appears in "aits shell submit --help".
func TestSubmitHelpDocumentsOutput(t *testing.T) {
	out := call{args: []string{"shell", "submit", "--help"}}.run(t)
	help := out.stdout
	row := func(name string) bool { // a table row starting with name
		return regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(name) + `( |$)`).MatchString(help)
	}
	word := func(name string) bool {
		return regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`).MatchString(help)
	}
	rt := reflect.TypeFor[shell.Result]()
	for i := range rt.NumField() {
		if name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ","); !row(name) {
			t.Errorf("result field %q is not documented in help", name)
		}
	}
	for _, s := range stringConsts(t, "../../internal/shell", "Status") {
		if !row(s) {
			t.Errorf("status %q is not documented in help", s)
		}
	}
	for _, c := range firstStringArgs(t, "../../internal/cli", "failure", "usageError") {
		if !word(c) {
			t.Errorf("error code %q is not documented in help", c)
		}
	}
}

// goFiles parses the non-test Go files of dir.
func goFiles(t *testing.T, dir string) []*ast.File {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return files
}

// stringConsts returns the values of the string constants of type typ in dir.
func stringConsts(t *testing.T, dir, typ string) []string {
	var vals []string
	for _, f := range goFiles(t, dir) {
		ast.Inspect(f, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			if id, ok := spec.Type.(*ast.Ident); !ok || id.Name != typ {
				return true
			}
			for _, v := range spec.Values {
				vals = append(vals, stringLit(t, v))
			}
			return true
		})
	}
	if len(vals) == 0 {
		t.Fatalf("no %s constants found in %s", typ, dir)
	}
	return vals
}

// firstStringArgs returns the first argument of every call to one of funcs in
// dir, which must be a string literal.
func firstStringArgs(t *testing.T, dir string, funcs ...string) []string {
	var vals []string
	for _, f := range goFiles(t, dir) {
		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok || len(c.Args) == 0 {
				return true
			}
			if id, ok := c.Fun.(*ast.Ident); ok && slices.Contains(funcs, id.Name) {
				vals = append(vals, stringLit(t, c.Args[0]))
			}
			return true
		})
	}
	if len(vals) == 0 {
		t.Fatalf("no calls to %v found in %s", funcs, dir)
	}
	slices.Sort(vals)
	return slices.Compact(vals)
}

func stringLit(t *testing.T, e ast.Expr) string {
	t.Helper()
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		t.Fatalf("expected a string literal, got %T", e)
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
