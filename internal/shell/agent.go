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
	// argv is the agent's arguments for r: a new conversation, or r.Session
	// when it is not empty, with r.Model when it is not empty, in the
	// vendor's sandbox when r.Write is set, allowing r.AllowTools. It never
	// carries the prompt (P8).
	argv func(r Request) []string
	// allowTools says whether the agent takes a list of tools to run without
	// asking (#121).
	allowTools bool
	env        []string // the agent's own variables, passed on top of baseEnv when set
	parse      parser   // turns one line of the agent's output into events
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

// claudeArgv gives each allowed tool its own --allowedTools, so a tool is one
// argv element and never joined with another (P8).
func claudeArgv(r Request) []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if r.Model != "" {
		args = append(args, "--model", r.Model)
	}
	for _, tool := range r.AllowTools {
		args = append(args, "--allowedTools", tool)
	}
	if r.Write {
		args = append(args, "--permission-mode", "acceptEdits", "--settings", claudeSandbox)
	}
	if r.Session != "" {
		args = append(args, "--resume", r.Session)
	}
	return args
}

// codexArgv always names the sandbox: Codex keeps a session's sandbox when it
// is resumed without one, so a session started with write access would keep
// it (P9). No --skip-git-repo-check or approval flags: Codex keeps its own
// checks.
func codexArgv(r Request) []string {
	sandbox := "read-only"
	if r.Write {
		sandbox = "workspace-write"
	}
	args := []string{"exec", "--json", "-s", sandbox}
	if r.Model != "" {
		args = append(args, "-m", r.Model)
	}
	if r.Session != "" {
		args = append(args, "resume", r.Session)
	}
	return append(args, "-")
}

var agents = map[string]agent{
	"claude": {
		name:       "claude",
		executable: "claude",
		argv:       claudeArgv,
		allowTools: true,
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

// Environ builds the environment of a child that is not an agent, such as a
// job's command (P8): the base allowlist and the names in pass, with values
// taken from parent.
func Environ(parent, pass []string) []string { return agent{}.environ(parent, pass) }

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
