//go:build !windows

package document

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestOpenRegularSourceDoesNotBlockAfterFIFOReplacement(t *testing.T) {
	root := directTempDir(t)
	input := filepath.Join(root, "raced.pdf")
	writeFixture(t, input, []byte("%PDF-1.7\n%%EOF\n"))

	type result struct {
		report  Report
		hookErr error
	}
	done := make(chan result, 1)
	go func() {
		var hookErr error
		file, _, err := openRegularSourceWithHook(input, func() {
			if removeErr := os.Remove(input); removeErr != nil {
				hookErr = removeErr
				return
			}
			hookErr = unix.Mkfifo(input, 0o600)
		})
		if file != nil {
			_ = file.Close()
		}
		done <- result{
			report:  acquisitionFailure(newReport("document_operation_fifo_race", 1024), err),
			hookErr: hookErr,
		}
	}()

	select {
	case got := <-done:
		if got.hookErr != nil {
			t.Fatalf("replace regular source with FIFO: %v", got.hookErr)
		}
		assertFailure(t, got.report, StateFailed, FailureInvalidInput)
	case <-time.After(time.Second):
		t.Fatal("opening a regular file replaced by a FIFO blocked")
	}

	if err := os.Remove(input); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove FIFO: %v", err)
	}
}
