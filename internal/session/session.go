package session

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// DefaultLimit is the number of results the command asks for by default.
const DefaultLimit = 20

// Query is one search.
type Query struct {
	Terms   []string      // all must occur in the session's text, ignoring case
	Agent   string        // one of Agents(), or "" for all
	Cwd     string        // absolute; only sessions in or below it, as given or with symlinks resolved; "" for any
	Since   time.Duration // only files modified within it; 0 for any
	Limit   int           // at most this many results; at least 1
	Environ []string      // KEY=VALUE, for HOME, CLAUDE_CONFIG_DIR and CODEX_HOME
}

// Result is one matching session.
type Result struct {
	Agent     string    `json:"agent"`
	SessionID string    `json:"session_id"`
	Cwd       string    `json:"cwd"`
	UpdatedAt time.Time `json:"updated_at"`
	Snippet   string    `json:"snippet"`
}

// Skipped counts what could not be read, so a partial answer is never silent
// (P6).
type Skipped struct {
	Files int `json:"files"`
	Lines int `json:"lines"`
}

// Output is the answer to a Query.
type Output struct {
	Results []Result `json:"results"`
	Skipped Skipped  `json:"skipped"`
}

// InvalidQueryError means the query was rejected before anything was read.
type InvalidQueryError struct{ Reason string }

func (e *InvalidQueryError) Error() string { return "invalid query: " + e.Reason }

func invalid(format string, a ...any) error {
	return &InvalidQueryError{Reason: fmt.Sprintf(format, a...)}
}

// Agents returns the agents whose transcripts are searched, sorted.
func Agents() []string { return slices.Sorted(maps.Keys(vendors)) }

func (q Query) validate() error {
	if len(q.Terms) == 0 {
		return invalid("no search terms")
	}
	if slices.Contains(q.Terms, "") {
		return invalid("empty search term")
	}
	if _, ok := vendors[q.Agent]; q.Agent != "" && !ok {
		return invalid("unknown agent %q (one of: %s)", q.Agent, strings.Join(Agents(), ", "))
	}
	if q.Cwd != "" && !filepath.IsAbs(q.Cwd) {
		return invalid("directory %q is not absolute", q.Cwd)
	}
	if q.Since < 0 {
		return invalid("since must be positive, got %s", q.Since)
	}
	if q.Limit < 1 {
		return invalid("limit must be at least 1, got %d", q.Limit)
	}
	return nil
}
