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
	args       []string // never carries the prompt (P8)
	env        []string // variables passed through when set
	envPrefix  []string // variables with one of these prefixes are passed through
}

var agents = map[string]agent{
	"claude": {
		name:       "claude",
		executable: "claude",
		args:       []string{"-p", "--output-format", "stream-json", "--verbose"},
		env: []string{
			"PATH", "HOME", "USER", "LOGNAME", "LANG", "TZ", "TMPDIR",
			"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
			"CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY",
		},
		envPrefix: []string{"LC_"},
	},
}

// Agents returns the supported agent names, sorted.
func Agents() []string { return slices.Sorted(maps.Keys(agents)) }

// environ builds the child's environment from scratch (P8): the agent's
// allowlist plus the names in pass, with values taken from parent.
func (a agent) environ(parent, pass []string) []string {
	var out []string
	for _, kv := range parent {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		hasPrefix := func(p string) bool { return strings.HasPrefix(name, p) }
		if slices.Contains(a.env, name) || slices.Contains(pass, name) || slices.ContainsFunc(a.envPrefix, hasPrefix) {
			out = append(out, kv)
		}
	}
	return out
}

// isSet reports whether environ has an entry for name.
func isSet(environ []string, name string) bool {
	return slices.ContainsFunc(environ, func(kv string) bool { return strings.HasPrefix(kv, name+"=") })
}
