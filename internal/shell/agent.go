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
	// argv is the agent's arguments: a new conversation, or session when it
	// is not empty, in the vendor's sandbox when write is set. It never
	// carries the prompt (P8).
	argv  func(session string, write bool) []string
	env   []string // the agent's own variables, passed on top of baseEnv when set
	parse parser   // turns one line of the agent's output into events
}

// baseEnv is passed to every agent when set, together with every LC_*
// variable (P8).
var baseEnv = []string{
	"PATH", "HOME", "USER", "LOGNAME", "LANG", "TZ", "TMPDIR",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
}

// claudeSandbox lets Claude Code edit files and run commands without asking,
// confined by its sandbox to the working directory and its temp directory,
// with no network; a command the sandbox would stop is never retried outside
// it, and a run where the sandbox cannot start fails (P6, P9).
const claudeSandbox = `{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true,"allowUnsandboxedCommands":false,"failIfUnavailable":true}}`

func claudeArgv(session string, write bool) []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if write {
		args = append(args, "--permission-mode", "acceptEdits", "--settings", claudeSandbox)
	}
	if session != "" {
		args = append(args, "--resume", session)
	}
	return args
}

// codexArgv always names the sandbox: Codex keeps a session's sandbox when it
// is resumed without one, so a session started with write access would keep
// it (P9). No --skip-git-repo-check or approval flags: Codex keeps its own
// checks.
func codexArgv(session string, write bool) []string {
	sandbox := "read-only"
	if write {
		sandbox = "workspace-write"
	}
	args := []string{"exec", "--json", "-s", sandbox}
	if session != "" {
		args = append(args, "resume", session)
	}
	return append(args, "-")
}

var agents = map[string]agent{
	"claude": {
		name:       "claude",
		executable: "claude",
		argv:       claudeArgv,
		env:        []string{"CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY"},
		parse:      parseClaude,
	},
	"codex": {
		name:       "codex",
		executable: "codex",
		argv:       codexArgv,
		// The credential variables Codex reads besides its login in CODEX_HOME.
		env:   []string{"CODEX_HOME", "OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN"},
		parse: parseCodex,
	},
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

// lookupEnv returns the value of name in environ, the last one as the child
// gets it, and whether name is set at all.
func lookupEnv(environ []string, name string) (val string, ok bool) {
	for _, kv := range environ {
		if v, found := strings.CutPrefix(kv, name+"="); found {
			val, ok = v, true
		}
	}
	return val, ok
}
