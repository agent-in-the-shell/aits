package shell

import (
	"maps"
	"slices"
	"strings"
)

// agent describes how to run one vendor CLI.
type agent struct {
	name       string
	executable string
	args       []string                 // never carries the prompt (P8)
	resume     func(id string) []string // the argv that continues session id
	env        []string                 // the agent's own variables, passed on top of baseEnv when set
	parse      parser                   // turns one line of the agent's output into events
}

// baseEnv is passed to every agent when set, together with every LC_*
// variable (P8).
var baseEnv = []string{
	"PATH", "HOME", "USER", "LOGNAME", "LANG", "TZ", "TMPDIR",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
}

var claudeArgs = []string{"-p", "--output-format", "stream-json", "--verbose"}

var agents = map[string]agent{
	"claude": {
		name:       "claude",
		executable: "claude",
		args:       claudeArgs,
		resume:     func(id string) []string { return append(slices.Clip(claudeArgs), "--resume", id) },
		env:        []string{"CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY"},
		parse:      parseClaude,
	},
	"codex": {
		name:       "codex",
		executable: "codex",
		// No --skip-git-repo-check, sandbox or approval flags: Codex keeps its
		// own safety defaults (P9).
		args:   []string{"exec", "--json", "-"},
		resume: func(id string) []string { return []string{"exec", "--json", "resume", id, "-"} },
		// The credential variables Codex reads besides its login in CODEX_HOME.
		env:   []string{"CODEX_HOME", "OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN"},
		parse: parseCodex,
	},
}

// argv returns the agent's arguments: a new conversation, or session when it
// is not empty.
func (a agent) argv(session string) []string {
	if session == "" {
		return a.args
	}
	return a.resume(session)
}

// Agents returns the supported agent names, sorted.
func Agents() []string { return slices.Sorted(maps.Keys(agents)) }

// environ builds the child's environment from scratch (P8): the base
// allowlist, the agent's own variables and the names in pass, with values
// taken from parent.
func (a agent) environ(parent, pass []string) []string {
	var out []string
	for _, kv := range parent {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if slices.Contains(baseEnv, name) || strings.HasPrefix(name, "LC_") ||
			slices.Contains(a.env, name) || slices.Contains(pass, name) {
			out = append(out, kv)
		}
	}
	return out
}

// isSet reports whether environ has an entry for name.
func isSet(environ []string, name string) bool {
	return slices.ContainsFunc(environ, func(kv string) bool { return strings.HasPrefix(kv, name+"=") })
}
