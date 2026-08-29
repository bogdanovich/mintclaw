package thread

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCopiedAttachmentSurvivesRestartWithoutSourcePathDisclosure(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	secretDirectory := filepath.Join(t.TempDir(), "private-location")
	if err := os.Mkdir(secretDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(secretDirectory, "report.txt")
	if err := os.WriteFile(source, []byte("bounded report\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, Mode: AttachmentModeCopy, At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if attachment.SourcePath != "" || attachment.Filename != "report.txt" || attachment.Size != 15 {
		t.Fatalf("attachment = %+v", attachment)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewStore(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	resolved, loaded, err := restarted.ResolveAttachment(t.Context(), metadata.ThreadID, attachment.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != attachment {
		t.Fatalf("loaded attachment = %+v, want %+v", loaded, attachment)
	}
	data, err := os.ReadFile(resolved)
	if err != nil || string(data) != "bounded report\n" {
		t.Fatalf("resolved content = %q, %v", data, err)
	}
	manifest, err := os.ReadFile(attachmentTestManifestPath(t, store, metadata.ThreadID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifest), secretDirectory) {
		t.Fatalf("copied manifest disclosed source directory: %s", manifest)
	}
}

func TestCopiedAttachmentDeduplicatesAndSurvivesOtherThreadDeletion(t *testing.T) {
	store, first := newLeaseTestThread(t)
	second, err := NewMetadata(NewThreadID(), first.Project, "second thread", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(second); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "shared.log")
	if err := os.WriteFile(source, []byte("same bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstLease := acquireAttachmentTestLease(t, store, first.ThreadID)
	firstAttachment, err := store.AdmitAttachment(t.Context(), firstLease, first, AttachmentInput{
		Path: source, Mode: AttachmentModeCopy, At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.AdmitAttachment(t.Context(), firstLease, first, AttachmentInput{
		Path: source, Mode: AttachmentModeCopy, At: time.Now().Add(time.Minute),
	})
	if err != nil || repeated.Ref != firstAttachment.Ref {
		t.Fatalf("repeated admission = %+v, %v", repeated, err)
	}
	secondLease := acquireAttachmentTestLease(t, store, second.ThreadID)
	secondAttachment, err := store.AdmitAttachment(t.Context(), secondLease, second, AttachmentInput{
		Path: source, Mode: AttachmentModeCopy, At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	firstPath, _, err := store.ResolveAttachment(t.Context(), first.ThreadID, firstAttachment.Ref)
	if err != nil {
		t.Fatal(err)
	}
	secondPath, _, err := store.ResolveAttachment(t.Context(), second.ThreadID, secondAttachment.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if firstPath != secondPath || firstAttachment.Ref == secondAttachment.Ref {
		t.Fatalf("paths/refs = %q %q / %q %q", firstPath, secondPath, firstAttachment.Ref, secondAttachment.Ref)
	}
	if err := secondLease.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TrashThread(firstLease, first.ThreadID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := firstLease.Release(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ResolveAttachment(t.Context(), second.ThreadID, secondAttachment.Ref); err != nil {
		t.Fatalf("shared blob disappeared after first thread deletion: %v", err)
	}
}

func TestExternalAttachmentReportsChangedMissingAndSymlinkedPaths(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	source := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(source, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, Mode: AttachmentModeExternal, At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	if attachment.SourcePath != canonicalSource {
		t.Fatalf("external path = %q, want %q", attachment.SourcePath, canonicalSource)
	}
	if _, _, err := store.ResolveAttachment(t.Context(), metadata.ThreadID, attachment.Ref); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("changed!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ResolveAttachment(
		t.Context(),
		metadata.ThreadID,
		attachment.Ref,
	); !IsAttachmentUnavailable(
		err,
	) {
		t.Fatalf("changed external error = %v", err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(t.TempDir(), "replacement.txt")
	if err := os.WriteFile(replacement, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacement, source); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := store.ResolveAttachment(
		t.Context(),
		metadata.ThreadID,
		attachment.Ref,
	); !IsAttachmentUnavailable(err) ||
		!strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlinked external error = %v", err)
	}
}

func TestAttachmentReferenceIsThreadScoped(t *testing.T) {
	store, first := newLeaseTestThread(t)
	second, err := NewMetadata(NewThreadID(), first.Project, "second thread", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(second); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(source, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	lease := acquireAttachmentTestLease(t, store, first.ThreadID)
	attachment, err := store.AdmitAttachment(t.Context(), lease, first, AttachmentInput{
		Path: source, Mode: AttachmentModeCopy, At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ResolveAttachment(
		t.Context(),
		second.ThreadID,
		attachment.Ref,
	); !IsAttachmentUnavailable(
		err,
	) {
		t.Fatalf("cross-thread resolve error = %v", err)
	}
}

func TestAttachmentAdmissionRejectsUnsafeOrOversizedInput(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	oversized := filepath.Join(t.TempDir(), "large.bin")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxAttachmentBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: oversized, Mode: AttachmentModeCopy, At: time.Now(),
	}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized admission error = %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.AdmitAttachment(canceled, lease, metadata, AttachmentInput{
		Path: oversized, Mode: AttachmentModeCopy, At: time.Now(),
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission error = %v", err)
	}
}

func TestAttachmentManifestFailsClosedWithoutRepair(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	threadRoot, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(threadRoot, attachmentDirectory)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, attachmentManifest)
	corrupt := []byte(`{"version":1,"thread_id":"` + metadata.ThreadID + `","entries":[],"unknown":true}`)
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListAttachments(metadata.ThreadID); err == nil {
		t.Fatal("ListAttachments() accepted unknown manifest state")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(corrupt) {
		t.Fatalf("corrupt manifest was rewritten: %q", after)
	}
}

func acquireAttachmentTestLease(t *testing.T, store *Store, threadID string) *Lease {
	t.Helper()
	lease, err := store.AcquireLease(threadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	return lease
}

func attachmentTestManifestPath(t *testing.T, store *Store, threadID string) string {
	t.Helper()
	threadRoot, err := store.ThreadRoot(threadID)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(threadRoot, attachmentDirectory, attachmentManifest)
}
