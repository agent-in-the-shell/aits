// Package session is the "session" command group: it searches the coding
// agents' own transcript files where they are and writes nothing.
package session

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"
)

// snippetSize bounds the text of a snippet, before the "…" marks.
const snippetSize = 160

// transcript is what search keeps of one file while reading it: its session
// id and working directory, which terms its text contains, and the newest
// text that contains the first term.
type transcript struct {
	sessionID, cwd string
	source         string   // the first source recorded, the vendor's own value
	terms          []string // lower-cased
	found          []bool
	snippetText    string
}

// parsed is what a parser found in one transcript line.
type parsed struct {
	id, cwd    string   // the session id and working directory it names, or ""
	source     string   // how the session was started, the vendor's value, or ""
	message    bool     // the line is a conversation message, searched or not
	texts      []string // the searched text in it
	notSession bool     // the file is not a conversation the user can continue
}

// parser reads one transcript line, first when it is the file's first
// non-blank line; ok is false when the line is not usable. Lines of shapes it
// does not search are usable and carry no text.
type parser func(line []byte, first bool) (p parsed, ok bool)

func newTranscript(terms []string) *transcript {
	t := &transcript{found: make([]bool, len(terms))}
	for _, term := range terms {
		t.terms = append(t.terms, strings.ToLower(term))
	}
	return t
}

// identify records the session id, working directory and source a line
// names, each unless already known: the first value wins.
func (t *transcript) identify(p parsed) {
	if t.sessionID == "" {
		t.sessionID = p.id
	}
	if t.cwd == "" {
		t.cwd = p.cwd
	}
	if t.source == "" {
		t.source = p.source
	}
}

// add records one piece of searched text.
func (t *transcript) add(text string) {
	low := strings.ToLower(text)
	for i, term := range t.terms {
		if strings.Contains(low, term) {
			t.found[i] = true
		}
	}
	if strings.Contains(low, t.terms[0]) {
		t.snippetText = text
	}
}

// matched reports whether every term occurred in some added text.
func (t *transcript) matched() bool { return !slices.Contains(t.found, false) }

// snippet returns up to snippetSize bytes of text around the first
// case-insensitive occurrence of term, cut on rune boundaries, whitespace
// collapsed, with "…" where text was cut.
func snippet(text, term string) string {
	at := 0
	low := strings.ToLower(text)
	if i := strings.Index(low, strings.ToLower(term)); i >= 0 {
		// ToLower maps rune to rune, so the rune index carries over.
		at = byteOffset(text, utf8.RuneCountInString(low[:i]))
	}
	start := max(0, at-snippetSize/3)
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	end := min(len(text), start+snippetSize)
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	s := strings.Join(strings.Fields(text[start:end]), " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(text) {
		s += "…"
	}
	return s
}

// byteOffset returns the byte offset of rune n of s.
func byteOffset(s string, n int) int {
	for i := range s {
		if n == 0 {
			return i
		}
		n--
	}
	return len(s)
}

// injected reports whether text, ignoring leading whitespace, starts with one
// of prefixes: text the agent CLI adds to a user turn by itself.
func injected(text string, prefixes []string) bool {
	text = strings.TrimLeft(text, " \t\r\n")
	return slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(text, p) })
}

// blockTexts returns the text of the content blocks whose type is one of
// kinds; ok is false when content is not a list of blocks.
func blockTexts(content json.RawMessage, kinds ...string) ([]string, bool) {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return nil, false
	}
	var out []string
	for _, b := range blocks {
		if slices.Contains(kinds, b.Type) {
			out = append(out, b.Text)
		}
	}
	return out, true
}

// ownText drops from a user turn the texts the agent CLI wrote by itself.
func ownText(texts, prefixes []string) []string {
	return slices.DeleteFunc(texts, func(s string) bool { return injected(s, prefixes) })
}
