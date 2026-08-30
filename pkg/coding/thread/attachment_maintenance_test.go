package thread

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAttachmentMaintenanceLeaseSerializesStoreInstances(t *testing.T) {
	store, _ := newLeaseTestThread(t)
	other, err := NewStore(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.acquireAttachmentMaintenanceLease()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.acquireAttachmentMaintenanceLease(); !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf("concurrent maintenance lease error = %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := other.acquireAttachmentMaintenanceLease()
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestAttachmentAdmissionDoesNotPublishWhileMaintenanceIsBusy(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	maintenance, err := store.acquireAttachmentMaintenanceLease()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pending.txt")
	if err := os.WriteFile(path, []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	if attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: path, At: time.Now(),
	}); !errors.Is(err, ErrLeaseBusy) || attachment != (Attachment{}) {
		t.Fatalf("busy admission = %+v, %v", attachment, err)
	}
	entries, err := store.ListAttachments(metadata.ThreadID)
	if err != nil || len(entries) != 0 {
		t.Fatalf("busy admission manifest = %+v, %v", entries, err)
	}
	if err := maintenance.Release(); err != nil {
		t.Fatal(err)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: path, At: time.Now(),
	})
	if err != nil || attachment.Ref == "" {
		t.Fatalf("admission after maintenance = %+v, %v", attachment, err)
	}
}
