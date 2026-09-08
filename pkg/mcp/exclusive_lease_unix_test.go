//go:build !windows

package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func assertExclusiveLeaseFileSecurity(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("lock file mode = %o, want 600", mode)
	}
}

func TestAcquireExclusiveServerLeaseRejectsSymlinkWithoutMutatingTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("unchanged"), 0o700); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chmod(target, 0o700); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	leasePath := filepath.Join(root, "playwright.lock")
	if err := os.Symlink(target, leasePath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	lease, err := AcquireExclusiveServerLease("playwright", leasePath)
	if err == nil {
		_ = lease.Close()
		t.Fatal("AcquireExclusiveServerLease() accepted a symbolic link")
	}
	info, statErr := os.Stat(target)
	if statErr != nil {
		t.Fatalf("Stat() error = %v", statErr)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("target permissions = %04o, want 0700", got)
	}
	contents, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if got := string(contents); got != "unchanged" {
		t.Fatalf("target contents = %q, want unchanged", got)
	}
}

func TestAcquireExclusiveServerLeaseRejectsSymlinkedParentWithoutMutatingTarget(t *testing.T) {
	root := t.TempDir()
	lockParent := filepath.Join(root, "locks")
	if err := os.Mkdir(lockParent, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	outside := t.TempDir()
	target := filepath.Join(outside, "playwright.lock")
	if err := os.WriteFile(target, []byte("unchanged"), 0o700); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chmod(target, 0o700); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	if err := os.Rename(lockParent, lockParent+"-validated"); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	if err := os.Symlink(outside, lockParent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	lease, err := AcquireExclusiveServerLease(
		"playwright",
		filepath.Join(lockParent, "playwright.lock"),
	)
	if err == nil {
		_ = lease.Close()
		t.Fatal("AcquireExclusiveServerLease() accepted a symlinked parent")
	}
	info, statErr := os.Stat(target)
	if statErr != nil {
		t.Fatalf("Stat() error = %v", statErr)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("target permissions = %04o, want 0700", got)
	}
	contents, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if got := string(contents); got != "unchanged" {
		t.Fatalf("target contents = %q, want unchanged", got)
	}
}
