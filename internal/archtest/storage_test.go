package archtest

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestOnlyStoreWrites enforces P7: only internal/store creates directories or
// writes files, so the root, the modes and the temp-file-plus-rename rule
// live in one place.
func TestOnlyStoreWrites(t *testing.T) {
	write := regexp.MustCompile(`\bos\.(WriteFile|MkdirAll|Create)\(`)
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
		if loc := write.FindIndex(src); loc != nil {
			t.Errorf("%s writes files outside internal/store: %s", path, src[loc[0]:loc[1]])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
