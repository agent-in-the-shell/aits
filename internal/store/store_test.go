package store

import (
	"os"
	"path/filepath"
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
