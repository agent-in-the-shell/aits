package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func samePath(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// --run-dir gives the task a directory of its own, the templates are filled
// when it runs, and a follow-up runs in the same directory (#137).
func TestTaskRunDir(t *testing.T) {
	q := newQueue(t, "ok")
	base := filepath.Join(t.TempDir(), "runs")
	id := created(t, q.run("create", "--agent", "claude", "--run-dir", base, "--json", "work in {{aits.run_dir}} on {{aits.date}} as {{aits.task_id}}; keep {{other}}"))
	if out := q.run("work"); out.code != 0 {
		t.Fatalf("work: code=%d stderr=%q", out.code, out.stderr)
	}
	want := filepath.Join(base, id)
	rec := readRecord(t, q.path)
	if !samePath(t, rec.Dir, want) {
		t.Errorf("the agent ran in %s, want %s", rec.Dir, want)
	}
	if p := "work in " + want + " on " + time.Now().Format("2006-01-02") + " as " + id + "; keep {{other}}"; rec.Stdin != p {
		t.Errorf("prompt %q, want %q", rec.Stdin, p)
	}
	if fi, err := os.Stat(want); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("run dir: %v %v", fi, err)
	}
	r := decodeOne[taskRecord](t, q.run("show", id, "--json").stdout)
	if r.RunDir != base || r.Run == nil || r.Run.Dir != want {
		t.Errorf("task %+v run %+v", r, r.Run)
	}
	f := q.after(id, "next")
	if out := q.run("work"); out.code != 0 {
		t.Fatalf("work follow-up: code=%d stderr=%q", out.code, out.stderr)
	}
	if rec := readRecord(t, q.path); !samePath(t, rec.Dir, want) {
		t.Errorf("follow-up %s ran in %s, want its parent's %s", f, rec.Dir, want)
	}
}

func TestSubmitRunDir(t *testing.T) {
	dir := vendor(t, "ok")
	base := t.TempDir()
	out := call{path: dir, args: submitArgs("--json", "--run-dir", base, "in {{aits.run_dir}}")}.run(t)
	r := decodeOne[submitResult](t, out.stdout)
	if out.code != 0 || filepath.Dir(r.Dir) != base || !samePath(t, readRecord(t, dir).Dir, r.Dir) || readRecord(t, dir).Stdin != "in "+r.Dir {
		t.Errorf("code=%d dir=%q record=%+v", out.code, r.Dir, readRecord(t, dir))
	}
}

// --worktree runs the agent in a new detached worktree of the repository;
// outside a repository the run never starts (#137).
func TestTaskWorktree(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "first"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	q := newQueue(t, "ok")
	q.path += string(os.PathListSeparator) + filepath.Dir(gitPath)
	id := created(t, q.run("create", "--agent", "claude", "--cwd", repo, "--worktree", "--json", "x"))
	if out := q.run("work"); out.code != 0 {
		t.Fatalf("work: code=%d stdout=%q", out.code, out.stdout)
	}
	want := filepath.Join(q.home, "worktree", id)
	if rec := readRecord(t, q.path[:strings.IndexByte(q.path, os.PathListSeparator)]); !samePath(t, rec.Dir, want) {
		t.Errorf("the agent ran in %s, want %s", rec.Dir, want)
	}
	list, _ := exec.Command("git", "-C", repo, "worktree", "list").Output()
	if !strings.Contains(string(list), id) {
		t.Errorf("git worktree list:\n%s", list)
	}

	plain := created(t, q.run("create", "--agent", "claude", "--cwd", t.TempDir(), "--worktree", "--json", "y"))
	q.run("work")
	if r := decodeOne[taskRecord](t, q.run("show", plain, "--json").stdout); r.Run == nil || r.Run.Status != "not_started" || !strings.Contains(r.Run.Message, "git worktree add") {
		t.Errorf("not a repository: %+v", r.Run)
	}
}

func TestRunDirRefuses(t *testing.T) {
	q := newQueue(t, "ok")
	id := q.create("x")
	for name, args := range map[string][]string{
		"unknown template": {"create", "--agent", "claude", "--json", "on {{aits.nope}}"},
		"cwd and run-dir":  {"create", "--agent", "claude", "--cwd", t.TempDir(), "--run-dir", t.TempDir(), "--json", "x"},
		"follow-up":        {"create", "--after", id, "--run-dir", t.TempDir(), "--json", "x"},
	} {
		out := q.run(args...)
		if e := decode(t, out.stdout).Error; out.code != 2 || e == nil || e.Code != "invalid_input" {
			t.Errorf("%s: code=%d stdout=%q", name, out.code, out.stdout)
		}
	}
	out := call{path: vendor(t, "ok"), args: submitArgs("--json", "{{aits.nope}}")}.run(t)
	if e := decode(t, out.stdout).Error; out.code != 2 || e == nil || !strings.Contains(e.Message, "{{aits.nope}}") {
		t.Errorf("submit: code=%d stdout=%q", out.code, out.stdout)
	}
}
