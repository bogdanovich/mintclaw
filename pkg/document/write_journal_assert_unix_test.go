//go:build !windows

package document

import (
	"os"
	"testing"
)

func assertPrivateDocumentJournalPath(t *testing.T, path string, directory bool) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("document journal path is not private: path=%q mode=%s", path, info.Mode())
	}
}
