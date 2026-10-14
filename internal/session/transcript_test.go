package session

import (
	"strings"
	"testing"
)

func TestTranscript(t *testing.T) {
	tr := newTranscript([]string{"Process", "timeout"})
	tr.add("we looked at the process group")
	if tr.matched() {
		t.Fatal("matched with one term missing")
	}
	tr.add("then the TIMEOUT fired")
	tr.add("process again, later")
	if !tr.matched() {
		t.Fatal("terms in different messages must match")
	}
	if tr.snippetText != "process again, later" {
		t.Errorf("snippet source = %q, want the newest text with the first term", tr.snippetText)
	}
	tr.identify(parsed{id: "s1", cwd: "/a", source: "cli"})
	tr.identify(parsed{id: "s2", cwd: "/b", source: "exec"})
	if tr.sessionID != "s1" || tr.cwd != "/a" || tr.source != "cli" {
		t.Errorf("identify kept %q %q %q, want the first ones", tr.sessionID, tr.cwd, tr.source)
	}
}

func TestSnippet(t *testing.T) {
	long := strings.Repeat("a ", 100) + "NEEDLE" + strings.Repeat(" b", 100)
	tests := []struct {
		name, text, term, want string
	}{
		{"short", "find the needle\nhere", "needle", "find the needle here"},
		{"case", "Find The NEEDLE", "needle", "Find The NEEDLE"},
		{"cut both ends", long, "needle", "…" + strings.Repeat("a ", 26) + "NEEDLE" + strings.Repeat(" b", 50) + "…"},
		{"rune boundary", strings.Repeat("é", 100) + "x", "x", "…" + strings.Repeat("é", 27) + "x"},
		{"no match keeps the start", "abc", "zzz", "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := snippet(tt.text, tt.term)
			if got != tt.want {
				t.Errorf("snippet = %q\nwant      %q", got, tt.want)
			}
			if body := strings.Trim(got, "…"); len(body) > snippetSize {
				t.Errorf("snippet body is %d bytes", len(body))
			}
		})
	}
}

func TestInjected(t *testing.T) {
	prefixes := []string{"<system-reminder>", "<command-"}
	for text, want := range map[string]bool{
		"  <system-reminder>x":    true,
		"<command-name>/clear":    true,
		"please fix <command-x>":  false,
		"<pasted_content>my text": false,
	} {
		if got := injected(text, prefixes); got != want {
			t.Errorf("injected(%q) = %v, want %v", text, got, want)
		}
	}
}
