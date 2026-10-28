package store

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "aits")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := Dir([]string{"HOME=/nowhere", "AITS_HOME=" + root}, "task")
	if err != nil || dir != filepath.Join(root, "task") {
		t.Fatalf("Dir = %q, %v", dir, err)
	}
	if m := mode(t, root); m != 0o700 {
		t.Errorf("root mode %v, want 0700 (tightened)", m)
	}
	if m := mode(t, dir); m != 0o700 {
		t.Errorf("dir mode %v, want 0700", m)
	}
	home := t.TempDir()
	if dir, err := Dir([]string{"HOME=" + home}, "task"); err != nil || dir != filepath.Join(home, ".local/share/aits/task") {
		t.Errorf("default Dir = %q, %v", dir, err)
	}
	if _, err := Dir(nil, "task"); err == nil {
		t.Error("Dir without HOME or AITS_HOME succeeded")
	}
}

func TestWriteFileLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.json")
	for _, data := range []string{"one", "two"} {
		if err := WriteFile(path, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(path); string(b) != "two" {
		t.Errorf("content %q", b)
	}
	if m := mode(t, path); m != 0o600 {
		t.Errorf("mode %v, want 0600", m)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only a.json", len(entries))
	}
}

func TestLock(t *testing.T) {
	dir := t.TempDir()
	unlock, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		u, err := Lock(dir)
		if err == nil {
			u()
		}
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("a second Lock did not wait for the first")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the second Lock did not get the lock after unlock")
	}
}

func TestTryLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.lock")
	if _, err := TryLock(path, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("TryLock(missing, create=false) = %v, want ErrNotExist", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("TryLock(create=false) created the file")
	}
	unlock, err := TryLock(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if m := mode(t, path); m != 0o600 {
		t.Errorf("mode %v, want 0600", m)
	}
	for _, create := range []bool{true, false} {
		if _, err := TryLock(path, create); !errors.Is(err, ErrLocked) {
			t.Errorf("second TryLock(create=%v) = %v, want ErrLocked", create, err)
		}
	}
	unlock()
	again, err := TryLock(path, false)
	if err != nil {
		t.Fatalf("TryLock after unlock = %v", err)
	}
	again()
	loose := filepath.Join(dir, "b.lock")
	if err := os.WriteFile(loose, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := TryLock(loose, true)
	if err != nil {
		t.Fatal(err)
	}
	u()
	if m := mode(t, loose); m != 0o600 {
		t.Errorf("existing file mode %v, want 0600 (tightened)", m)
	}
}

// A lock file that is not a regular file is an error, never a lock: a symlink
// to another lock would lock the wrong file, and a FIFO is not a lock file
// (on Linux it can even be flocked).
func TestTryLockRefusesOddFiles(t *testing.T) {
	dir := t.TempDir()
	free := filepath.Join(dir, "free.lock")
	unlock, err := TryLock(free, true)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	link := filepath.Join(dir, "link.lock")
	if err := os.Symlink(free, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo.lock")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, fifo} {
		for _, create := range []bool{true, false} {
			if u, err := TryLock(path, create); err == nil || errors.Is(err, ErrLocked) || errors.Is(err, os.ErrNotExist) {
				if err == nil {
					u()
				}
				t.Errorf("TryLock(%s, create=%v) = %v, want another error", filepath.Base(path), create, err)
			}
		}
	}
}

func TestOpenAppendAndRotate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	for i := range 5 {
		if err := Rotate(path, 4, 3); err != nil {
			t.Fatalf("rotate %d: %v", i, err)
		}
		f, err := OpenAppend(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(string(rune('a'+i)) + "1234"); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	for suffix, want := range map[string]string{"": "e1234", ".1": "d1234", ".2": "c1234", ".3": "b1234"} {
		if b, err := os.ReadFile(path + suffix); err != nil || string(b) != want {
			t.Errorf("x.log%s = %q, %v; want %q", suffix, b, err, want)
		}
	}
	if _, err := os.Stat(path + ".4"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("x.log.4 kept: %v", err)
	}
	if m := mode(t, path); m != 0o600 {
		t.Errorf("mode %v, want 0600", m)
	}
	link := filepath.Join(t.TempDir(), "link.log")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAppend(link); err == nil {
		t.Error("OpenAppend followed a symlink")
	}
}
