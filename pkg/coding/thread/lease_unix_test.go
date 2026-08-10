//go:build unix && !aix

package thread

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestThreadLeaseFileIsPrivate(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatalf("AcquireLease() error = %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	path := filepath.Join(store.Root(), "threads", metadata.ThreadID, leaseFileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(thread.lock) error = %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("thread.lock mode = %o, want 600", mode)
	}
}

func TestThreadLeaseRejectsFIFOWithoutBlocking(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	path := filepath.Join(store.Root(), "threads", metadata.ThreadID, leaseFileName)
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Skipf("Mkfifo unavailable: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		lease, err := store.AcquireLease(metadata.ThreadID)
		if lease != nil {
			_ = lease.Release()
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("AcquireLease() accepted FIFO lock file")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AcquireLease() blocked on FIFO lock file")
	}
}

func TestThreadLeaseRejectsLinkedLockFile(t *testing.T) {
	for _, testCase := range []struct {
		name string
		link func(target, path string) error
	}{
		{name: "symlink", link: os.Symlink},
		{name: "hardlink", link: os.Link},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store, metadata := newLeaseTestThread(t)
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.Root(), "threads", metadata.ThreadID, leaseFileName)
			if err := testCase.link(outside, path); err != nil {
				t.Skipf("link unavailable: %v", err)
			}
			if lease, err := store.AcquireLease(metadata.ThreadID); err == nil {
				_ = lease.Release()
				t.Fatal("AcquireLease() accepted linked lock file")
			}
			data, err := os.ReadFile(outside)
			if err != nil || string(data) != "outside" {
				t.Fatalf("outside file changed: %q / %v", data, err)
			}
		})
	}
}
