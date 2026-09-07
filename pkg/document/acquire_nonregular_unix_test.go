//go:build !windows

package document

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestAcquireSnapshotRejectsFIFOAndDevice(t *testing.T) {
	root := directTempDir(t)
	scratch := filepath.Join(root, "protected")
	fifo := filepath.Join(root, "input.pdf")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("create FIFO: %v", err)
	}
	_, fifoReport := acquireSnapshot(
		t.Context(), fifo, scratch, newReport("document_operation_fifo", DefaultMaxInputBytes), nil,
	)
	assertFailure(t, fifoReport, StateFailed, FailureInvalidInput)

	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("device fixture is unavailable")
	}
	_, deviceReport := acquireSnapshot(
		t.Context(), "/dev/null", scratch, newReport("document_operation_device", DefaultMaxInputBytes), nil,
	)
	assertFailure(t, deviceReport, StateFailed, FailureInvalidInput)
}
