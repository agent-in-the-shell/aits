package task

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-in-the-shell/aits/internal/store"
)

func env(t *testing.T) []string {
	return []string{"HOME=" + t.TempDir(), "AITS_HOME=" + t.TempDir(), "PATH=" + t.TempDir()}
}

func create(t *testing.T, environ []string, agent string) Task {
	t.Helper()
	tk, err := Create(environ, Spec{Agent: agent, Cwd: t.TempDir(), Prompt: "hi", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestCreateValidates(t *testing.T) {
	environ := env(t)
	dir := t.TempDir()
	for name, s := range map[string]Spec{
		"no agent":      {Cwd: dir, Prompt: "hi", Timeout: time.Minute},
		"unknown agent": {Agent: "nope", Cwd: dir, Prompt: "hi", Timeout: time.Minute},
		"blank prompt":  {Agent: "claude", Cwd: dir, Prompt: " ", Timeout: time.Minute},
		"relative cwd":  {Agent: "claude", Cwd: "rel", Prompt: "hi", Timeout: time.Minute},
		"missing cwd":   {Agent: "claude", Cwd: filepath.Join(dir, "x"), Prompt: "hi", Timeout: time.Minute},
		"zero timeout":  {Agent: "claude", Cwd: dir, Prompt: "hi"},
		"bad env name":  {Agent: "claude", Cwd: dir, Prompt: "hi", Timeout: time.Minute, Env: []string{"A=B"}},
	} {
		var invalid *InvalidError
		if _, err := Create(environ, s); !errors.As(err, &invalid) {
			t.Errorf("%s: err = %v, want *InvalidError", name, err)
		}
	}
}

// Work claims the oldest queued task, of the given agent if one is named;
// with nothing to run it returns nil. The fake PATH has no agent, so each
// run ends not_started — enough to see which task was claimed.
func TestWorkClaimOrder(t *testing.T) {
	environ := env(t)
	a := create(t, environ, "claude")
	b := create(t, environ, "codex")
	got, err := Work(context.Background(), environ, "codex")
	if err != nil || got == nil || got.ID != b.ID {
		t.Fatalf("Work(codex) = %+v, %v; want %s", got, err, b.ID)
	}
	if got, _ := Work(context.Background(), environ, ""); got == nil || got.ID != a.ID {
		t.Fatalf("Work() claimed %+v, want %s", got, a.ID)
	}
	if got, err := Work(context.Background(), environ, ""); got != nil || err != nil {
		t.Fatalf("Work on an empty queue = %+v, %v", got, err)
	}
}

func TestWorkNotStarted(t *testing.T) {
	environ := env(t)
	tk, err := Create(environ, Spec{Agent: "claude", Cwd: t.TempDir(), Prompt: "hi", Timeout: time.Minute, Env: []string{"AITS_TEST_UNSET"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Work(context.Background(), environ, "")
	if err != nil || got.ID != tk.ID || got.Status != Failed || got.Run == nil || got.Run.Status != NotStarted || got.Run.Message == "" || got.Lease != nil {
		t.Fatalf("Work = %+v, %v", got, err)
	}
}

func TestReclaim(t *testing.T) {
	environ := env(t)
	expired := create(t, environ, "claude")
	live := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	for _, x := range []struct {
		tk      Task
		expires time.Time
	}{{expired, time.Now().Add(-time.Second)}, {live, time.Now().Add(time.Hour)}} {
		x.tk.Status, x.tk.Lease = Running, &Lease{Token: "w", WorkerPID: 1, ExpiresAt: x.expires}
		if err := write(dir, x.tk); err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := List(environ)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("List = %+v, %v", tasks, err)
	}
	if e := tasks[0]; e.Status != Failed || e.Run == nil || e.Run.Status != LeaseExpired || e.Lease != nil {
		t.Errorf("expired task = %+v", e)
	}
	if l := tasks[1]; l.Status != Running || l.Lease == nil {
		t.Errorf("live task = %+v", l)
	}
}

// A worker whose lease was reclaimed must not overwrite the failed record.
func TestUpdateForeignToken(t *testing.T) {
	environ := env(t)
	tk := create(t, environ, "claude")
	dir, _ := store.Dir(environ, "task")
	tk.Status, tk.Lease = Running, &Lease{Token: "other"}
	if err := write(dir, tk); err != nil {
		t.Fatal(err)
	}
	if _, err := update(dir, tk.ID, "mine", func(t *Task) { t.Status = Done }); !errors.Is(err, ErrReclaimed) {
		t.Fatalf("update = %v, want ErrReclaimed", err)
	}
	if got, _ := Show(environ, tk.ID); got.Status != Running {
		t.Errorf("record overwritten: %+v", got)
	}
}

func TestShowUnknown(t *testing.T) {
	var invalid *InvalidError
	for _, id := range []string{"nope", "../../etc/passwd"} {
		if _, err := Show(env(t), id); !errors.As(err, &invalid) {
			t.Errorf("Show(%q) err = %v", id, err)
		}
	}
}
