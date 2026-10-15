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
	wantGroup := "Usage: root group COMMAND\n\nA group.\n\nCommands:\n  leaf  A leaf.\n\nRun 'root group COMMAND --help' for details.\n" +
		"\nErrors: unknown_command, unknown_flag (exit 2).\n"
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
