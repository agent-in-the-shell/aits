package session

import (
	"bytes"
	"io"
	"strings"
)

// chunkSize is how much of a file the prefilter reads at a time.
const chunkSize = 1 << 20

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
func mayMatch(r io.Reader, needles [][]byte) (bool, error) {
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
	chunk := make([]byte, chunkSize)
	window := make([]byte, 0, overlap+chunkSize)
	for {
		n, err := io.ReadFull(r, chunk)
		window = append(window, chunk[:n]...)
		low := bytes.ToLower(window)
		for i, nd := range needles {
			if !found[i] && bytes.Contains(low, nd) {
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
