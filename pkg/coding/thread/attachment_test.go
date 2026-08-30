package thread

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

func TestAttachmentSurvivesSourceChangesAndRestartWithoutPathDisclosure(t *testing.T) {
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
		Path: source, At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Filename != "report.txt" || attachment.Size != 15 {
		t.Fatalf("attachment = %+v", attachment)
	}
	if err := os.WriteFile(source, []byte("later source content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, _, err := store.ResolveAttachment(t.Context(), metadata.ThreadID, attachment.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if data, readErr := os.ReadFile(resolved); readErr != nil || string(data) != "bounded report\n" {
		t.Fatalf("resolved changed-source content = %q, %v", data, readErr)
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
		t.Fatalf("attachment manifest disclosed source directory: %s", manifest)
	}
}

func TestAttachmentDeduplicatesAndSurvivesOtherThreadDeletion(t *testing.T) {
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
		Path: source, At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.AdmitAttachment(t.Context(), firstLease, first, AttachmentInput{
		Path: source, At: time.Now().Add(time.Minute),
	})
	if err != nil || repeated.Ref != firstAttachment.Ref {
		t.Fatalf("repeated admission = %+v, %v", repeated, err)
	}
	secondLease := acquireAttachmentTestLease(t, store, second.ThreadID)
	secondAttachment, err := store.AdmitAttachment(t.Context(), secondLease, second, AttachmentInput{
		Path: source, At: time.Now(),
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

func TestAttachmentDeduplicationRejectsReplacedThread(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	source := filepath.Join(t.TempDir(), "attachment.txt")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, At: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	threadRoot, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(attachmentTestManifestPath(t, store, metadata.ThreadID))
	if err != nil {
		t.Fatal(err)
	}
	movedRoot := threadRoot + "-moved"
	if err := os.Rename(threadRoot, movedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(threadRoot, attachmentDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(threadRoot, leaseFileName), []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(threadRoot, attachmentDirectory, attachmentManifest),
		manifest,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, At: time.Now(),
	})
	if err == nil || attachment.Ref != "" || !strings.Contains(err.Error(), "active thread") {
		t.Fatalf("replacement deduplication = %+v, %v", attachment, err)
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
		Path: source, At: time.Now(),
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
		Path: oversized, At: time.Now(),
	}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized admission error = %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.AdmitAttachment(canceled, lease, metadata, AttachmentInput{
		Path: oversized, At: time.Now(),
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission error = %v", err)
	}
}

func TestAttachmentAdmissionPreservesWhitespaceInSourcePath(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	directory := t.TempDir()
	plain := filepath.Join(directory, "report")
	spaced := filepath.Join(directory, "report ")
	if err := os.WriteFile(plain, []byte("wrong"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spaced, []byte("right"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: spaced, Filename: "report.txt", At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, _, err := store.ResolveAttachment(t.Context(), metadata.ThreadID, attachment.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(resolved); err != nil || string(data) != "right" {
		t.Fatalf("resolved whitespace path content = %q, %v", data, err)
	}
}

func TestResolveAttachmentPreservesContextCancellation(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	source := filepath.Join(t.TempDir(), "attachment.txt")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := store.ResolveAttachment(
		canceled,
		metadata.ThreadID,
		attachment.Ref,
	); !errors.Is(err, context.Canceled) ||
		IsAttachmentUnavailable(err) {
		t.Fatalf("canceled resolve error = %v", err)
	}
}

func TestAttachmentAdmissionReturnsCommittedReference(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	source := filepath.Join(t.TempDir(), "attachment.txt")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected post-rename sync failure")
	writeRoot := store.writeRoot
	store.writeRoot = func(root *os.Root, name string, data []byte, mode os.FileMode) error {
		if err := writeRoot(root, name, data, mode); err != nil {
			return err
		}
		if name == attachmentManifest {
			return &fileutil.CommittedWriteError{Err: injected}
		}
		return nil
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, At: time.Now(),
	})
	if attachment.Ref == "" || !IsCommittedAttachmentError(err) || !errors.Is(err, injected) {
		t.Fatalf("committed admission = %+v, %v", attachment, err)
	}
	entries, listErr := store.ListAttachments(metadata.ThreadID)
	if listErr != nil || len(entries) != 1 || entries[0].Ref != attachment.Ref {
		t.Fatalf("committed manifest = %+v, %v", entries, listErr)
	}
}

func TestAttachmentAdmissionReportsCommittedDirectoryCreation(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	source := filepath.Join(t.TempDir(), "attachment.txt")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected directory sync failure")
	threadRoot, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	syncRoot := store.syncRoot
	store.syncRoot = func(root *os.Root) error {
		if filepath.Clean(root.Name()) == threadRoot {
			return injected
		}
		return syncRoot(root)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, At: time.Now(),
	})
	if attachment.Ref != "" || !fileutil.IsCommittedWriteError(err) ||
		IsCommittedAttachmentError(err) || !errors.Is(err, injected) {
		t.Fatalf("directory creation admission = %+v, %v", attachment, err)
	}
	entries, listErr := store.ListAttachments(metadata.ThreadID)
	if listErr != nil || len(entries) != 0 {
		t.Fatalf("manifest changed after directory sync failure = %+v, %v", entries, listErr)
	}
}

func TestAttachmentAdmissionDoesNotPublishIntoReplacedThread(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	source := filepath.Join(t.TempDir(), "attachment.txt")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	threadRoot, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	movedRoot := threadRoot + "-moved"
	replaced := false
	syncRoot := store.syncRoot
	store.syncRoot = func(root *os.Root) error {
		if !replaced {
			replaced = true
			if err := os.Rename(threadRoot, movedRoot); err != nil {
				return err
			}
			if err := os.Mkdir(threadRoot, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(
				filepath.Join(threadRoot, leaseFileName),
				[]byte("replacement\n"),
				0o600,
			); err != nil {
				return err
			}
		}
		return syncRoot(root)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, At: time.Now(),
	})
	if err == nil || attachment.Ref != "" {
		t.Fatalf("replaced-thread admission = %+v, %v", attachment, err)
	}
	for _, root := range []string{threadRoot, movedRoot} {
		manifest := filepath.Join(root, attachmentDirectory, attachmentManifest)
		if _, statErr := os.Stat(manifest); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("manifest published under %q: %v", root, statErr)
		}
	}
}

func TestAttachmentAdmissionRejectsDetachedBlobDirectory(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	content := []byte("content")
	source := filepath.Join(t.TempDir(), "attachment.txt")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	shardRoot := filepath.Join(store.Root(), "blobs", "sha256", digest[:2])
	movedRoot := shardRoot + "-moved"
	writeRoot := store.writeRoot
	store.writeRoot = func(root *os.Root, name string, data []byte, mode os.FileMode) error {
		if err := writeRoot(root, name, data, mode); err != nil {
			return err
		}
		if name != digest {
			return nil
		}
		if err := os.Rename(shardRoot, movedRoot); err != nil {
			return err
		}
		return os.Mkdir(shardRoot, 0o700)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, At: time.Now(),
	})
	if err == nil || attachment.Ref != "" || !strings.Contains(err.Error(), "changed during publication") {
		t.Fatalf("detached blob admission = %+v, %v", attachment, err)
	}
	entries, listErr := store.ListAttachments(metadata.ThreadID)
	if listErr != nil || len(entries) != 0 {
		t.Fatalf("manifest changed after detached blob publication = %+v, %v", entries, listErr)
	}
}

func TestAttachmentAdmissionRejectsDetachedManifestDirectory(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	source := filepath.Join(t.TempDir(), "attachment.txt")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	threadRoot, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	attachmentsRoot := filepath.Join(threadRoot, attachmentDirectory)
	movedRoot := attachmentsRoot + "-moved"
	writeRoot := store.writeRoot
	store.writeRoot = func(root *os.Root, name string, data []byte, mode os.FileMode) error {
		if err := writeRoot(root, name, data, mode); err != nil {
			return err
		}
		if name != attachmentManifest {
			return nil
		}
		if err := os.Rename(attachmentsRoot, movedRoot); err != nil {
			return err
		}
		return os.Mkdir(attachmentsRoot, 0o700)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, At: time.Now(),
	})
	if err == nil || attachment.Ref != "" || !strings.Contains(err.Error(), "changed during publication") {
		t.Fatalf("detached manifest admission = %+v, %v", attachment, err)
	}
	entries, listErr := store.ListAttachments(metadata.ThreadID)
	if listErr != nil || len(entries) != 0 {
		t.Fatalf("active manifest changed after detached publication = %+v, %v", entries, listErr)
	}
}

func TestAttachmentAdmissionRejectsThreadDetachedAfterManifestWrite(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	source := filepath.Join(t.TempDir(), "attachment.txt")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	threadRoot, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	movedRoot := threadRoot + "-moved"
	writeRoot := store.writeRoot
	store.writeRoot = func(root *os.Root, name string, data []byte, mode os.FileMode) error {
		if err := writeRoot(root, name, data, mode); err != nil {
			return err
		}
		if name != attachmentManifest {
			return nil
		}
		if err := os.Rename(threadRoot, movedRoot); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(threadRoot, attachmentDirectory), 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(threadRoot, leaseFileName), []byte("replacement\n"), 0o600)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, At: time.Now(),
	})
	if err == nil || attachment.Ref != "" || !strings.Contains(err.Error(), "active thread") {
		t.Fatalf("detached thread admission = %+v, %v", attachment, err)
	}
	entries, listErr := store.ListAttachments(metadata.ThreadID)
	if listErr != nil || len(entries) != 0 {
		t.Fatalf("active manifest changed after detached thread publication = %+v, %v", entries, listErr)
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

func TestAttachmentManifestRejectsReplacedThreadsDirectory(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	source := filepath.Join(t.TempDir(), "attachment.txt")
	if err := os.WriteFile(source, []byte("attachment"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, At: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	threads := filepath.Join(store.Root(), "threads")
	moved := filepath.Join(store.Root(), "threads-owned")
	if err := os.Rename(threads, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, threads); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := store.ListAttachments(metadata.ThreadID); err == nil {
		t.Fatal("ListAttachments() followed a replaced threads directory")
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
