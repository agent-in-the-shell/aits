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
		if slices.Contains(a.env, name) || slices.Contains(pass, name) || hasAnyPrefix(name, a.envPrefix) {
			out = append(out, kv)
		}
	}
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// lookup returns the value of name in environ.
func lookup(environ []string, name string) (string, bool) {
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}
