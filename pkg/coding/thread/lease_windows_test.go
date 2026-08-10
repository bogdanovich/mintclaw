//go:build windows

package thread

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsThreadLeaseRejectsHardLinkWithoutMutatingTarget(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	threadRoot, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	const content = "must remain unchanged"
	targetPath := filepath.Join(threadRoot, "hard-link-target.txt")
	if err := os.WriteFile(targetPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(threadRoot, leaseFileName)
	if err := os.Link(targetPath, lockPath); err != nil {
		t.Fatal(err)
	}

	lease, acquireErr := store.AcquireLease(metadata.ThreadID)
	if lease != nil {
		_ = lease.Release()
		t.Fatal("AcquireLease() accepted a multiply linked Windows lock file")
	}
	if acquireErr == nil || !strings.Contains(acquireErr.Error(), "multiple hard links") {
		t.Fatalf("AcquireLease() error = %v", acquireErr)
	}
	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("hard-link target was mutated: %q", got)
	}
}
