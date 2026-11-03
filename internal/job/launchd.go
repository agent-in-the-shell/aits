package job

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/agent-in-the-shell/aits/internal/shell"
	"github.com/agent-in-the-shell/aits/internal/store"
)

// plistMark starts every LaunchAgent apply writes; apply replaces or removes
// only a plist that starts with it.
const plistMark = `<?xml version="1.0" encoding="UTF-8"?>
<!-- Written by aits job apply: edit the jobs file and run job apply again, not this file. -->
`

// SchedApplied is what an apply changed, or would change, about the
// LaunchAgent that runs job sched for its file (#143).
type SchedApplied struct {
	Label  string `json:"label"`
	Plist  string `json:"plist"`
	Change string `json:"change"` // added, changed, removed or unchanged
}

// agent is the LaunchAgent of one jobs file.
type agent struct {
	label, plist, domain string
	launchctl            string
	env                  []string
}

func newAgent(file, home string, env []string) (agent, error) {
	if home == "" {
		return agent{}, invalid("HOME is not set, so there is no ~/Library/LaunchAgents")
	}
	launchctl, err := shell.LookPath(env, "launchctl")
	if err != nil {
		return agent{}, errors.New("launchctl is not on PATH")
	}
	label := "aits.sched." + schedID(file)
	return agent{
		label:     label,
		plist:     filepath.Join(home, "Library", "LaunchAgents", label+".plist"),
		domain:    fmt.Sprintf("gui/%d", os.Getuid()),
		launchctl: launchctl,
		env:       env,
	}, nil
}

// render is the plist that runs aits job sched file, with AITS_HOME passed
// when aits has it, so sched and the caller's job list use one directory.
func (a agent) render(aits, file, log, aitsHome string) string {
	var b strings.Builder
	b.WriteString(plistMark)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	str := func(s string) string { return "<string>" + xmlEscape(s) + "</string>" }
	key := func(k string) { fmt.Fprintf(&b, "\t<key>%s</key>\n", k) }
	key("Label")
	fmt.Fprintf(&b, "\t%s\n", str(a.label))
	key("ProgramArguments")
	b.WriteString("\t<array>\n")
	for _, arg := range []string{aits, "job", "sched", file, "--json"} {
		fmt.Fprintf(&b, "\t\t%s\n", str(arg))
	}
	b.WriteString("\t</array>\n")
	if aitsHome != "" {
		key("EnvironmentVariables")
		fmt.Fprintf(&b, "\t<dict>\n\t\t<key>AITS_HOME</key>\n\t\t%s\n\t</dict>\n", str(aitsHome))
	}
	key("RunAtLoad")
	b.WriteString("\t<true/>\n")
	key("KeepAlive")
	b.WriteString("\t<true/>\n")
	key("StandardOutPath")
	fmt.Fprintf(&b, "\t%s\n", str(log))
	key("StandardErrorPath")
	fmt.Fprintf(&b, "\t%s\n", str(log))
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// read returns the installed plist, "" if there is none, and refuses one
// apply did not write.
func (a agent) read() (string, error) {
	b, err := os.ReadFile(a.plist)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !bytes.HasPrefix(b, []byte(plistMark)) {
		return "", invalid("%s was not written by job apply; move it away first", a.plist)
	}
	return string(b), nil
}

// loaded reports whether launchd has the agent.
func (a agent) loaded(ctx context.Context) bool {
	return a.run(ctx, "print", a.domain+"/"+a.label) == nil
}

// install writes text as the plist and (re)loads it.
func (a agent) install(ctx context.Context, text string) error {
	if a.loaded(ctx) {
		if err := a.run(ctx, "bootout", a.domain+"/"+a.label); err != nil {
			return err
		}
	}
	if err := store.MkdirAll(filepath.Dir(a.plist), 0o755); err != nil {
		return err
	}
	if err := store.WriteFile(a.plist, []byte(text)); err != nil {
		return err
	}
	return a.run(ctx, "bootstrap", a.domain, a.plist)
}

// remove unloads the agent and deletes its plist.
func (a agent) remove(ctx context.Context) error {
	if a.loaded(ctx) {
		if err := a.run(ctx, "bootout", a.domain+"/"+a.label); err != nil {
			return err
		}
	}
	return store.Remove(a.plist)
}

func (a agent) run(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, crontabTimeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, a.launchctl, args...)
	cmd.Env, cmd.Stderr = a.env, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launchctl %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
