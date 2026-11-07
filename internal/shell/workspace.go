package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/agent-in-the-shell/aits/internal/store"
)

// gitTimeout bounds the git worktree add a --worktree run starts with.
const gitTimeout = 60 * time.Second

// templateVars are the names a prompt may use as {{aits.NAME}} (#137).
var templateVars = []string{"date", "task_id", "run_dir"}

// checkTemplate refuses a {{aits.NAME}} whose NAME aits does not fill; any
// other text, {{ included, is the prompt's own.
func checkTemplate(prompt string) error {
	for rest := prompt; ; {
		i := strings.Index(rest, "{{aits.")
		if i < 0 {
			return nil
		}
		rest = rest[i+len("{{aits."):]
		name, _, ok := strings.Cut(rest, "}}")
		if !ok || !slices.Contains(templateVars, name) {
			if !ok || len(name) > 40 {
				name = firstLine(rest, 40)
			}
			return invalid("the prompt uses {{aits.%s}}; aits fills only {{aits.date}}, {{aits.task_id}} and {{aits.run_dir}}", name)
		}
	}
}

// expand fills the prompt's {{aits.NAME}} for a run that starts now in dir.
func expand(prompt, id, dir string) string {
	return strings.NewReplacer(
		"{{aits.date}}", time.Now().Format("2006-01-02"),
		"{{aits.task_id}}", id,
		"{{aits.run_dir}}", dir,
	).Replace(prompt)
}

// workspace makes the directory a run with RunDir works in: RunDir/RunID, a
// new directory, or with Worktree a detached git worktree of Dir there.
func workspace(ctx context.Context, r Request, env []string) (string, error) {
	dir := filepath.Join(r.RunDir, r.RunID)
	if err := store.MkdirAll(r.RunDir, 0o700); err != nil {
		return "", fmt.Errorf("run directory: %w", err)
	}
	if !r.Worktree {
		if err := store.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("run directory: %w", err)
		}
		return dir, nil
	}
	git, err := lookPath(env, "git")
	if err != nil {
		return "", fmt.Errorf("--worktree needs git on PATH")
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, git, "-C", r.Dir, "worktree", "add", "--detach", dir)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, &out, &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git worktree add: %v: %s", err, firstLine(strings.TrimSpace(out.String()), 300))
	}
	return dir, nil
}

// RunBase prepares the base a run's own directory goes under (#137): base,
// made absolute and created, or for a worktree run without one
// $AITS_HOME/worktree.
func RunBase(environ []string, base string, worktree bool) (string, error) {
	if base == "" {
		if !worktree {
			return "", nil
		}
		dir, err := store.Dir(environ, "worktree")
		if errors.Is(err, store.ErrNoRoot) {
			return "", invalid("%v", err)
		}
		return dir, err
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return "", invalid("run directory %q: %v", base, err)
	}
	if err := store.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("run directory: %w", err)
	}
	return abs, nil
}
