package thread

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestAttachmentMaintenanceLeaseUsesStoreDirectorySync(t *testing.T) {
	store, _ := newLeaseTestThread(t)
	syncCalls := 0
	store.syncDir = func(path string) error {
		if path != store.Root() {
			t.Fatalf("maintenance sync path = %q, want %q", path, store.Root())
		}
		syncCalls++
		return nil
	}
	lease, err := store.acquireAttachmentMaintenanceLease()
	if err != nil {
		t.Fatal(err)
	}
	if syncCalls != 1 {
		t.Fatalf("maintenance directory sync calls = %d, want 1", syncCalls)
	}
	if err := lease.Release(); err != nil {
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

func TestAttachmentAdmissionRejectsCommitAfterMaintenanceAuthorityReplacement(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	now := time.Now().UTC()
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	source := filepath.Join(t.TempDir(), "replacement.txt")
	content := []byte("replacement authority payload")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	published := make(chan struct{})
	resume := make(chan struct{})
	resumed := false
	t.Cleanup(func() {
		if !resumed {
			close(resume)
		}
	})
	store.afterAttachmentBlobPublication = func() {
		close(published)
		<-resume
	}
	type admission struct {
		attachment Attachment
		err        error
	}
	completed := make(chan admission, 1)
	go func() {
		attachment, admitErr := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
			Path: source, At: now,
		})
		completed <- admission{attachment: attachment, err: admitErr}
	}()
	select {
	case <-published:
	case <-time.After(10 * time.Second):
		t.Fatal("attachment admission did not publish its blob")
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(
		filepath.Join(store.Root(), "blobs", "sha256", digest[:2], digest),
		old,
		old,
	); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Rename(attachmentMaintenanceDirectory, attachmentMaintenanceDirectory+"-old"); err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	if err := root.Mkdir(attachmentMaintenanceDirectory, 0o700); err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := NewStore(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	collected, err := replacement.CollectAttachmentGarbage(t.Context(), AttachmentGCOptions{
		Before: time.Now().Add(-time.Hour), Delete: true,
	})
	if err != nil || collected.DeletedBlobs != 1 {
		t.Fatalf("replacement-authority sweep = %+v, %v", collected, err)
	}
	close(resume)
	resumed = true
	var outcome admission
	select {
	case outcome = <-completed:
	case <-time.After(10 * time.Second):
		t.Fatal("attachment admission did not reject the replaced authority")
	}
	if outcome.attachment != (Attachment{}) || outcome.err == nil ||
		!strings.Contains(outcome.err.Error(), "lock directory") {
		t.Fatalf("replaced-authority admission = %+v, %v", outcome.attachment, outcome.err)
	}
	entries, err := replacement.ListAttachments(metadata.ThreadID)
	if err != nil || len(entries) != 0 {
		t.Fatalf("replaced-authority manifest = %+v, %v", entries, err)
	}
}
