package archtest

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// storeOnly matches the os functions that create, move, change or remove a
// file or directory, called or taken as a value. OpenFile is matched whatever
// its flags: code that only reads uses os.Open or store.OpenRegular.
var storeOnly = regexp.MustCompile(`\bos\.(OpenFile|Mkdir|MkdirAll|MkdirTemp|Create|CreateTemp|WriteFile|Rename|Symlink|Link|Chmod|Chown|Lchown|Chtimes|Truncate|Remove|RemoveAll)\b`)

// TestOnlyStoreWrites enforces P7: only internal/store creates, moves,
// changes or removes files and directories, so the root, the modes and the
// temp-file-plus-rename rule live in one place.
func TestOnlyStoreWrites(t *testing.T) {
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "testdata" || d.Name() == ".git" || path == filepath.Join("..", "..", "internal", "store")) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, loc := range storeOnly.FindAllIndex(src, -1) {
			line := bytes.Count(src[:loc[0]], []byte("\n")) + 1
			t.Errorf("%s:%d writes files outside internal/store: %s", path, line, src[loc[0]:loc[1]])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestStoreOnlyCalls checks the pattern TestOnlyStoreWrites applies: every
// os function that creates, moves, changes or removes a file or directory is
// caught, called or taken as a value; reads and unrelated names are not.
func TestStoreOnlyCalls(t *testing.T) {
	caught := []string{
		"os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o644)",
		"os.OpenFile(p, os.O_RDONLY, 0)",
		"os.Mkdir(p, 0o755)",
		"os.MkdirAll(p, 0o755)",
		"os.MkdirTemp(dir, \"x\")",
		"os.Create(p)",
		"os.CreateTemp(dir, \"x\")",
		"os.WriteFile(p, b, 0o644)",
		"os.Rename(a, b)",
		"os.Symlink(a, b)",
		"os.Link(a, b)",
		"os.Chmod(p, 0o644)",
		"os.Chown(p, 0, 0)",
		"os.Lchown(p, 0, 0)",
		"os.Chtimes(p, t, t)",
		"os.Truncate(p, 0)",
		"os.Remove(p)",
		"os.RemoveAll(p)",
		"mv := os.Rename",
	}
	for _, src := range caught {
		if !storeOnly.MatchString(src) {
			t.Errorf("%s is not caught", src)
		}
	}
	allowed := []string{
		"os.Open(p)",
		"os.ReadFile(p)",
		"os.ReadDir(p)",
		"os.Readlink(p)",
		"os.Lstat(p)",
		"var e *os.LinkError",
		"os.O_CREATE",
		"store.Rename(a, b)",
		"pos.Remove(p)",
	}
	for _, src := range allowed {
		if storeOnly.MatchString(src) {
			t.Errorf("%s is caught", src)
		}
	}
}
