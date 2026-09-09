package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExclusiveServerLeaseIsNonBlockingAndReleasable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playwright.lock")
	lease, err := acquireExclusiveServerLease("playwright", path)
	if err != nil {
		t.Fatalf("acquireExclusiveServerLease(first) error = %v", err)
	}
	t.Cleanup(lease.release)

	assertExclusiveLeaseFileSecurity(t, path)

	contender, err := acquireExclusiveServerLease("playwright", path)
	if contender != nil {
		contender.release()
		t.Fatal("acquireExclusiveServerLease(contender) returned a lease")
	}
	var busyErr *ExclusiveLeaseBusyError
	if !errors.As(err, &busyErr) || busyErr.Server != "playwright" {
		t.Fatalf("acquireExclusiveServerLease(contender) error = %v, want busy classification", err)
	}
	if !errors.Is(err, errExclusiveLeaseBusy) {
		t.Fatalf("acquireExclusiveServerLease(contender) error = %v, want busy sentinel", err)
	}

	lease.release()
	reacquired, err := acquireExclusiveServerLease("playwright", path)
	if err != nil {
		t.Fatalf("acquireExclusiveServerLease(after release) error = %v", err)
	}
	reacquired.release()
}

func TestExclusiveServerLeaseKeepsDistinctPathsIndependent(t *testing.T) {
	root := t.TempDir()
	first, err := acquireExclusiveServerLease("first", filepath.Join(root, "first.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()
	second, err := acquireExclusiveServerLease("second", filepath.Join(root, "second.lock"))
	if err != nil {
		t.Fatal(err)
	}
	second.release()
}

func TestExclusiveServerLeaseRetainsCanonicalNamespaceReservation(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "locks")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "playwright.lock")
	lease, err := acquireExclusiveServerLease("playwright", path)
	if err != nil {
		t.Fatal(err)
	}
	moved := parent + "-moved"
	renameErr := os.Rename(parent, moved)
	if renameErr == nil {
		if err = os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	contender, contenderErr := acquireExclusiveServerLease("playwright", path)
	if contender != nil {
		contender.release()
		t.Fatal("canonical contender acquired a second lease")
	}
	if !errors.Is(contenderErr, errExclusiveLeaseBusy) {
		t.Fatalf("canonical contender error = %v, want busy", contenderErr)
	}
	if renameErr != nil {
		if err = lease.close(); err != nil {
			t.Fatalf("release after denied parent rename error = %v", err)
		}
		return
	}
	if err = lease.close(); !errors.Is(err, errExclusiveLeaseUnsafe) {
		t.Fatalf("release while parent is rebound error = %v, want unsafe", err)
	}
	if err = os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(moved, parent); err != nil {
		t.Fatal(err)
	}
	if err = lease.close(); err != nil {
		t.Fatalf("release after parent restore error = %v", err)
	}
}
