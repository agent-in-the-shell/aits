package session

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// chunkSize is how much of a file the prefilter reads at a time.
const chunkSize = 1 << 20

// buffers are one worker's reusable buffers, so reading a file allocates
// nothing new: per-file and per-chunk allocations otherwise cost more time in
// the garbage collector than in the search itself.
type buffers struct {
	chunk, window, low, line []byte
	reader                   *bufio.Reader
}

func newBuffers() *buffers {
	return &buffers{chunk: make([]byte, chunkSize), reader: bufio.NewReaderSize(nil, 64<<10)}
}

// needles returns the lower-cased terms the byte prefilter looks for. A term
// with a byte JSON escapes (quote, backslash, control character) would not
// match the raw file, so it is left to the parse step.
func needles(terms []string) [][]byte {
	var out [][]byte
	for _, t := range terms {
		if strings.ContainsFunc(t, func(r rune) bool { return r == '"' || r == '\\' || r < 0x20 }) {
			continue
		}
		out = append(out, []byte(strings.ToLower(t)))
	}
	return out
}

// mayMatch reports whether every needle occurs in r, ignoring case. It reads r
// chunk by chunk, keeping an overlap so a needle split between two chunks is
// still seen; memory does not grow with the file (P8).
func mayMatch(r io.Reader, needles [][]byte, b *buffers) (bool, error) {
	if len(needles) == 0 {
		return true, nil
	}
	longest := 0
	for _, n := range needles {
		longest = max(longest, len(n))
	}
	// Lower-casing can shrink a rune (up to 3 bytes to 1), so keep enough of
	// the previous chunk to hold the longest needle's original spelling.
	overlap := 4 * longest
	found := make([]bool, len(needles))
	left := len(needles)
	window := b.window[:0]
	defer func() { b.window = window[:0] }()
	for {
		n, err := io.ReadFull(r, b.chunk)
		window = append(window, b.chunk[:n]...)
		b.low = lowerInto(b.low[:0], window)
		for i, nd := range needles {
			if !found[i] && bytes.Contains(b.low, nd) {
				found[i] = true
				left--
			}
		}
		switch {
		case left == 0:
			return true, nil
		case err == io.EOF || err == io.ErrUnexpectedEOF:
			return false, nil
		case err != nil:
			return false, err
		}
		if len(window) > overlap {
			window = append(window[:0], window[len(window)-overlap:]...)
		}
	}
}

// lowerInto appends the lower-case form of s to dst, rune by rune as
// bytes.ToLower does, with ASCII bytes taken on a fast path. Bytes that are
// not valid UTF-8 are copied as they are.
func lowerInto(dst, s []byte) []byte {
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			dst = append(dst, c)
			i++
			continue
		}
		r, size := utf8.DecodeRune(s[i:])
		if r == utf8.RuneError && size == 1 {
			dst = append(dst, c)
		} else {
			dst = utf8.AppendRune(dst, unicode.ToLower(r))
		}
		i += size
	}
	return dst
}
