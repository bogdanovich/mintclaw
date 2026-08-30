//go:build unix

package thread

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAttachmentAdmissionRejectsFIFOWithoutBlocking(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	source := filepath.Join(t.TempDir(), "attachment.fifo")
	if err := unix.Mkfifo(source, 0o600); err != nil {
		t.Skipf("Mkfifo unavailable: %v", err)
	}
	type result struct {
		attachment Attachment
		err        error
	}
	done := make(chan result, 1)
	go func() {
		attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
			Path: source, At: time.Now(),
		})
		done <- result{attachment: attachment, err: err}
	}()
	select {
	case got := <-done:
		if got.err == nil || got.attachment.Ref != "" {
			t.Fatalf("FIFO admission = %+v, %v", got.attachment, got.err)
		}
	case <-time.After(2 * time.Second):
		writer, _ := os.OpenFile(source, os.O_WRONLY|unix.O_NONBLOCK, 0)
		if writer != nil {
			_ = writer.Close()
		}
		t.Fatal("attachment admission blocked while opening a FIFO")
	}
}
