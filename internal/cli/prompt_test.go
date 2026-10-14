package cli

import (
	"context"
	"strings"
	"testing"
)

// endless reads as an endless run of 'p', counting what it gave.
type endless struct{ n int }

func (e *endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'p'
	}
	e.n += len(p)
	return len(p), nil
}

// A prompt from stdin is bounded (P8): up to maxPrompt bytes are taken, more
// is invalid input, and reading stops at the bound, so an endless producer
// does not keep aits busy (#7).
func TestReadPromptBound(t *testing.T) {
	ctx := context.Background()
	if got, err := readPrompt(ctx, strings.NewReader(strings.Repeat("p", maxPrompt))); err != nil || len(got) != maxPrompt {
		t.Errorf("exactly the bound: %d bytes, %v", len(got), err)
	}
	if _, err := readPrompt(ctx, strings.NewReader(strings.Repeat("p", maxPrompt+1))); errCode(err) != "invalid_input" {
		t.Errorf("one byte over: %v, want invalid_input", err)
	}
	r := &endless{}
	if _, err := readPrompt(ctx, r); errCode(err) != "invalid_input" || r.n > maxPrompt+1<<20 {
		t.Errorf("endless input: %v after reading %d bytes", err, r.n)
	}
}
