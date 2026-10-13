package shell

import (
	"strings"
	"testing"
)

func TestTailBuffer(t *testing.T) {
	tb := &tailBuffer{max: 8}
	for _, s := range []string{"abc", "defgh", "ijk"} {
		if n, err := tb.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", s, n, err)
		}
	}
	if got := tb.String(); got != "defghijk" {
		t.Errorf("tail = %q, want %q", got, "defghijk")
	}
	big := &tailBuffer{max: 4}
	_, _ = big.Write([]byte(strings.Repeat("x", 100) + "é"))
	if got := big.String(); got != "xxé" {
		t.Errorf("tail = %q, want %q", got, "xxé")
	}
}
