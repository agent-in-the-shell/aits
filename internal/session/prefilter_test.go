package session

import (
	"bytes"
	"strings"
	"testing"
)

func TestNeedles(t *testing.T) {
	got := needles([]string{"Process", `a"b`, `c\d`, "ok"})
	if len(got) != 2 || string(got[0]) != "process" || string(got[1]) != "ok" {
		t.Errorf("needles = %q", got)
	}
}

func TestMayMatch(t *testing.T) {
	pad := strings.Repeat("x", chunkSize-3)
	tests := []struct {
		name  string
		text  string
		terms []string
		want  bool
	}{
		{"all present", "the Process group timed out", []string{"process", "timed"}, true},
		{"one missing", "the process group", []string{"process", "timeout"}, false},
		{"split across chunks", pad + "NEEDLE" + pad, []string{"needle"}, true},
		{"terms in different chunks", "alpha" + pad + pad + "omega", []string{"alpha", "omega"}, true},
		{"no needles", "anything", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ns [][]byte
			for _, term := range tt.terms {
				ns = append(ns, []byte(term))
			}
			got, err := mayMatch(bytes.NewReader([]byte(tt.text)), ns, newBuffers())
			if err != nil || got != tt.want {
				t.Errorf("mayMatch = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

// lowerInto must lower-case exactly as bytes.ToLower does on valid UTF-8, so
// the prefilter never disagrees with the matcher.
func TestLowerInto(t *testing.T) {
	for _, s := range []string{"", "ASCII Only", "Kelvin \u212a and İstanbul", "ΣΊΣΥΦΟΣ", "中文 Ü ẞ ǅ", strings.Repeat("Aé", 1000)} {
		if got, want := lowerInto(nil, []byte(s)), bytes.ToLower([]byte(s)); !bytes.Equal(got, want) {
			t.Errorf("lowerInto(%q) = %q, want %q", s, got, want)
		}
	}
}
