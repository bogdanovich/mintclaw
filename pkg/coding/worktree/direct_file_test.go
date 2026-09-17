package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSameDirectRegularFileRejectsSymlinkReplacementToSameInode(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "handoff.json")
	alternate := filepath.Join(directory, "preserved-handoff.json")
	if err := os.WriteFile(path, []byte("handoff\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, alternate); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(alternate, path); err != nil {
		t.Fatal(err)
	}
	current, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}

	if !os.SameFile(entry, opened) {
		t.Fatal("fixture did not preserve the opened inode")
	}
	if sameDirectRegularFile(entry, opened, current) {
		t.Fatal("symlink replacement to the opened inode was accepted")
	}
}
