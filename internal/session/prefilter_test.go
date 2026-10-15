package session

import (
	"bytes"
	"context"
	"errors"
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
			got, err := mayMatch(context.Background(), bytes.NewReader([]byte(tt.text)), ns, newBuffers())
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

// cancelOnRead returns bytes forever and cancels its context on the first
// Read, like a device or a file far larger than the time a user waits.
type cancelOnRead struct {
	cancel func()
	n      int
}

func (r *cancelOnRead) Read(p []byte) (int, error) {
	if r.n == 0 {
		r.cancel()
	}
	for i := range p {
		p[i] = 'x'
	}
	r.n += len(p)
	return len(p), nil
}

func TestMayMatchStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &cancelOnRead{cancel: cancel}
	ok, err := mayMatch(ctx, r, [][]byte{[]byte("needle")}, newBuffers())
	if ok || !errors.Is(err, context.Canceled) || r.n > 2*chunkSize {
		t.Errorf("mayMatch = %v, %v after reading %d bytes; want context.Canceled within %d", ok, err, r.n, 2*chunkSize)
	}
}

func TestEachLineStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := eachLine(ctx, strings.NewReader("a\nb\nc\n"), newBuffers(), func([]byte) (bool, bool) {
		calls++
		cancel()
		return true, true
	})
	if calls != 1 || !errors.Is(err, context.Canceled) {
		t.Errorf("eachLine called fn %d times, err %v; want 1 and context.Canceled", calls, err)
	}
}

// fn ends the reading by returning more false; the line still counts (#51).
func TestEachLineStopsWhenToldTo(t *testing.T) {
	calls := 0
	bad, err := eachLine(context.Background(), strings.NewReader("a\nb\nc\n"), newBuffers(), func([]byte) (bool, bool) {
		calls++
		return false, calls < 2
	})
	if calls != 2 || bad != 2 || err != nil {
		t.Errorf("eachLine called fn %d times, bad %d, err %v; want 2, 2, nil", calls, bad, err)
	}
}
