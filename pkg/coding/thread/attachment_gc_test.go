package thread

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAttachmentGarbageCollectionMarksActiveAndTrashBeforeSweep(t *testing.T) {
	store, active := newLeaseTestThread(t)
	now := time.Now().UTC()
	activeLease, err := store.AcquireLease(active.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	activeAttachment := admitGCTestAttachment(t, store, activeLease, active, "active.txt", "active", now)
	orphan := admitGCTestAttachment(t, store, activeLease, active, "orphan.txt", "orphan", now)
	if err := store.RemoveAttachmentRefs(t.Context(), activeLease, active, []string{orphan.Ref}); err != nil {
		t.Fatal(err)
	}
	if err := activeLease.Release(); err != nil {
		t.Fatal(err)
	}

	trashed, err := NewMetadata(NewThreadID(), active.Project, "trashed attachment", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(trashed); err != nil {
		t.Fatal(err)
	}
	trashLease, err := store.AcquireLease(trashed.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	trashAttachment := admitGCTestAttachment(t, store, trashLease, trashed, "trash.txt", "trash", now)
	if _, err := store.TrashThread(trashLease, trashed.ThreadID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := trashLease.Release(); err != nil {
		t.Fatal(err)
	}

	old := now.Add(-48 * time.Hour)
	for _, attachment := range []Attachment{activeAttachment, orphan, trashAttachment} {
		path := attachmentTestBlobPath(store, attachment)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	options := AttachmentGCOptions{Before: now.Add(-24 * time.Hour)}
	plan, err := store.CollectAttachmentGarbage(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ScannedManifests != 2 || plan.ReferencedBlobs != 2 || plan.ScannedBlobs != 3 ||
		len(plan.Candidates) != 1 || plan.Candidates[0].SHA256 != orphan.SHA256 ||
		plan.DeletedBlobs != 0 {
		t.Fatalf("garbage plan = %+v", plan)
	}

	options.Delete = true
	result, err := store.CollectAttachmentGarbage(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedBlobs != 1 || result.DeletedBytes != orphan.Size {
		t.Fatalf("garbage result = %+v", result)
	}
	if _, err := os.Stat(attachmentTestBlobPath(store, orphan)); !os.IsNotExist(err) {
		t.Fatalf("orphan blob remains: %v", err)
	}
	data, _, err := store.ResolveAttachment(t.Context(), active.ThreadID, activeAttachment.Ref)
	if err != nil || string(data) != "active" {
		t.Fatalf("active attachment after sweep = %q, %v", data, err)
	}
	if _, err := os.Stat(attachmentTestBlobPath(store, trashAttachment)); err != nil {
		t.Fatalf("recoverable trash blob removed: %v", err)
	}
}

func TestAttachmentGarbageCollectionKeepsDigestReferencedByAnotherThread(t *testing.T) {
	store, first := newLeaseTestThread(t)
	now := time.Now().UTC()
	second, err := NewMetadata(NewThreadID(), first.Project, "second shared attachment", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(second); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "shared.txt")
	if err := os.WriteFile(source, []byte("shared bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	firstLease, err := store.AcquireLease(first.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	firstAttachment, err := store.AdmitAttachment(t.Context(), firstLease, first, AttachmentInput{
		Path: source, At: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveAttachmentRefs(t.Context(), firstLease, first, []string{firstAttachment.Ref}); err != nil {
		t.Fatal(err)
	}
	if err := firstLease.Release(); err != nil {
		t.Fatal(err)
	}

	secondLease, err := store.AcquireLease(second.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	secondAttachment, err := store.AdmitAttachment(t.Context(), secondLease, second, AttachmentInput{
		Path: source, At: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := secondLease.Release(); err != nil {
		t.Fatal(err)
	}
	if firstAttachment.Ref == secondAttachment.Ref || firstAttachment.SHA256 != secondAttachment.SHA256 {
		t.Fatalf("shared attachment identities = %+v, %+v", firstAttachment, secondAttachment)
	}

	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(attachmentTestBlobPath(store, firstAttachment), old, old); err != nil {
		t.Fatal(err)
	}
	result, err := store.CollectAttachmentGarbage(t.Context(), AttachmentGCOptions{
		Before: now.Add(-24 * time.Hour), Delete: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 0 || result.DeletedBlobs != 0 || result.ReferencedBlobs != 1 {
		t.Fatalf("shared-digest sweep = %+v", result)
	}
	data, _, err := store.ResolveAttachment(t.Context(), second.ThreadID, secondAttachment.Ref)
	if err != nil || string(data) != "shared bytes" {
		t.Fatalf("surviving shared attachment = %q, %v", data, err)
	}
}

func TestAttachmentGarbageCollectionMarksQuarantinedForkPreparation(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	now := time.Now().UTC()
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	attachment := admitGCTestAttachment(t, store, lease, metadata, "fork.txt", "fork payload", now)
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	threadRoot, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	quarantineRoot := filepath.Join(store.Root(), "trash", "fork-preparations")
	if err := os.MkdirAll(quarantineRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(threadRoot, filepath.Join(quarantineRoot, metadata.ThreadID+"-failed")); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(attachmentTestBlobPath(store, attachment), old, old); err != nil {
		t.Fatal(err)
	}

	result, err := store.CollectAttachmentGarbage(t.Context(), AttachmentGCOptions{
		Before: now.Add(-24 * time.Hour), Delete: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ScannedManifests != 1 || result.ReferencedBlobs != 1 ||
		len(result.Candidates) != 0 || result.DeletedBlobs != 0 {
		t.Fatalf("quarantined-fork sweep = %+v", result)
	}
	if _, err := os.Stat(attachmentTestBlobPath(store, attachment)); err != nil {
		t.Fatalf("quarantined fork blob removed: %v", err)
	}
}

func TestAttachmentGarbageCollectionDeleteIsNoOpWithoutBlobStore(t *testing.T) {
	store, _ := newLeaseTestThread(t)
	result, err := store.CollectAttachmentGarbage(t.Context(), AttachmentGCOptions{
		Before: time.Now().Add(-24 * time.Hour), Delete: true,
	})
	if err != nil || result.ScannedBlobs != 0 || len(result.Candidates) != 0 || result.DeletedBlobs != 0 {
		t.Fatalf("empty GC result = %+v, %v", result, err)
	}
}

func TestAttachmentGarbageCollectionHonorsRetentionAndFailsClosedOnCorruptManifest(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	now := time.Now().UTC()
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	orphan := admitGCTestAttachment(t, store, lease, metadata, "recent.txt", "recent", now)
	if err := store.RemoveAttachmentRefs(t.Context(), lease, metadata, []string{orphan.Ref}); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	plan, err := store.CollectAttachmentGarbage(t.Context(), AttachmentGCOptions{
		Before: now.Add(-time.Hour), Delete: true,
	})
	if err != nil || len(plan.Candidates) != 0 || plan.DeletedBlobs != 0 {
		t.Fatalf("retained recent blob = %+v, %v", plan, err)
	}
	if _, err := os.Stat(attachmentTestBlobPath(store, orphan)); err != nil {
		t.Fatalf("recent orphan removed: %v", err)
	}

	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(attachmentTestBlobPath(store, orphan), old, old); err != nil {
		t.Fatal(err)
	}
	manifestPath := attachmentTestManifestPath(t, store, metadata.ThreadID)
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := store.CollectAttachmentGarbage(t.Context(), AttachmentGCOptions{
		Before: now.Add(-24 * time.Hour), Delete: true,
	})
	if err == nil || result.DeletedBlobs != 0 || IsCommittedAttachmentGCError(err) {
		t.Fatalf("corrupt-manifest sweep = %+v, %v", result, err)
	}
	if _, err := os.Stat(attachmentTestBlobPath(store, orphan)); err != nil {
		t.Fatalf("orphan removed after failed mark: %v", err)
	}
}

func TestAttachmentGarbageCollectionClassifiesDeletionDurabilityFailure(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	now := time.Now().UTC()
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "orphan.txt")
	if err := os.WriteFile(source, []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphan, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{Path: source, At: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveAttachmentRefs(t.Context(), lease, metadata, []string{orphan.Ref}); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(attachmentTestBlobPath(store, orphan), old, old); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected shard sync failure")
	syncRoot := store.syncRoot
	shardSyncs := 0
	store.syncRoot = func(root *os.Root) error {
		if filepath.Base(root.Name()) == orphan.SHA256[:2] {
			shardSyncs++
			if shardSyncs == 3 {
				return injected
			}
		}
		return syncRoot(root)
	}
	result, err := store.CollectAttachmentGarbage(t.Context(), AttachmentGCOptions{
		Before: now.Add(-24 * time.Hour), Delete: true,
	})
	if !errors.Is(err, injected) || !IsCommittedAttachmentGCError(err) || result.DeletedBlobs != 1 {
		t.Fatalf("committed sweep = %+v, %v", result, err)
	}
}

func TestAttachmentGarbageCollectionRejectsUnknownBlobEntriesWithoutDeletion(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	now := time.Now().UTC()
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	orphan := admitGCTestAttachment(t, store, lease, metadata, "orphan.txt", "orphan", now)
	if err := store.RemoveAttachmentRefs(t.Context(), lease, metadata, []string{orphan.Ref}); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(attachmentTestBlobPath(store, orphan), old, old); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(store.Root(), "blobs", "sha256", orphan.SHA256[:2], "README")
	if err := os.WriteFile(unknown, []byte("unknown"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := store.CollectAttachmentGarbage(t.Context(), AttachmentGCOptions{
		Before: now.Add(-24 * time.Hour), Delete: true,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid blob entry") || result.DeletedBlobs != 0 {
		t.Fatalf("unknown-entry sweep = %+v, %v", result, err)
	}
	if _, err := os.Stat(attachmentTestBlobPath(store, orphan)); err != nil {
		t.Fatalf("orphan removed after unknown entry: %v", err)
	}
}

func TestAttachmentGarbageCollectionRejectsCorruptBlobWithoutDeletion(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	now := time.Now().UTC()
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	orphan := admitGCTestAttachment(t, store, lease, metadata, "orphan.txt", "orphan", now)
	if err := store.RemoveAttachmentRefs(t.Context(), lease, metadata, []string{orphan.Ref}); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	path := attachmentTestBlobPath(store, orphan)
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	result, err := store.CollectAttachmentGarbage(t.Context(), AttachmentGCOptions{
		Before: now.Add(-24 * time.Hour), Delete: true,
	})
	if err == nil || result.DeletedBlobs != 0 || IsCommittedAttachmentGCError(err) ||
		!strings.Contains(err.Error(), "content identity changed") {
		t.Fatalf("corrupt-blob sweep = %+v, %v", result, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("corrupt blob removed: %v", err)
	}
}

func TestAttachmentGarbageCollectionRehashesCandidateBeforeDeletion(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	now := time.Now().UTC()
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	orphan := admitGCTestAttachment(t, store, lease, metadata, "orphan.txt", "orphan", now)
	if err := store.RemoveAttachmentRefs(t.Context(), lease, metadata, []string{orphan.Ref}); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	path := attachmentTestBlobPath(store, orphan)
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	session, err := store.acquireAttachmentGCSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	marked, _, err := store.markAttachmentGarbage(t.Context(), session.root)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := store.planAttachmentGarbage(t.Context(), session.root, AttachmentGCOptions{
		Before: now.Add(-24 * time.Hour), BlobLimit: 10, CandidateLimit: 10,
	}, marked)
	if err != nil || len(plan.Candidates) != 1 {
		t.Fatalf("replacement plan = %+v, %v", plan, err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, plan.Candidates[0].ModifiedAt, plan.Candidates[0].ModifiedAt); err != nil {
		t.Fatal(err)
	}
	result, err := store.sweepAttachmentGarbage(t.Context(), session, plan)
	if err == nil || result.DeletedBlobs != 0 || IsCommittedAttachmentGCError(err) ||
		!strings.Contains(err.Error(), "content identity changed") {
		t.Fatalf("replaced-candidate sweep = %+v, %v", result, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("replaced candidate removed: %v", err)
	}
}

func TestAttachmentGarbageCollectionBindsSweepToMarkedStoreRoot(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	now := time.Now().UTC()
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	orphan := admitGCTestAttachment(t, store, lease, metadata, "orphan.txt", "orphan", now)
	if err := store.RemoveAttachmentRefs(t.Context(), lease, metadata, []string{orphan.Ref}); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	path := attachmentTestBlobPath(store, orphan)
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	session, err := store.acquireAttachmentGCSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	marked, _, err := store.markAttachmentGarbage(t.Context(), session.root)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := store.planAttachmentGarbage(t.Context(), session.root, AttachmentGCOptions{
		Before: now.Add(-24 * time.Hour), BlobLimit: 10, CandidateLimit: 10,
	}, marked)
	if err != nil || len(plan.Candidates) != 1 {
		t.Fatalf("root-replacement plan = %+v, %v", plan, err)
	}
	movedRoot := store.Root() + "-replaced"
	if err := os.Rename(store.Root(), movedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(store.Root(), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := store.sweepAttachmentGarbage(t.Context(), session, plan)
	if err == nil || result.DeletedBlobs != 0 || IsCommittedAttachmentGCError(err) ||
		!strings.Contains(err.Error(), "store root changed") {
		t.Fatalf("root-replacement sweep = %+v, %v", result, err)
	}
	relative, err := filepath.Rel(store.Root(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(movedRoot, relative)); err != nil {
		t.Fatalf("detached-store candidate removed: %v", err)
	}
}

func TestAttachmentGarbageCollectionEnforcesAuthorityBudgetsBeforeExpansion(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	_ = admitGCTestAttachment(t, store, lease, metadata, "bounded.txt", "bounded", time.Now().UTC())
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	session, err := store.acquireAttachmentGCSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	marked := make(map[string]struct{})
	count, err := scanAttachmentManifestTree(
		t.Context(), store, session.root, []string{"threads"}, 0, attachmentGCMarkedBlobLimit, marked,
	)
	if err == nil || count != 0 || len(marked) != 0 || !strings.Contains(err.Error(), "exceeds 0 entries") {
		t.Fatalf("zero manifest budget = count %d marked %d err %v", count, len(marked), err)
	}
	count, err = scanAttachmentManifestTree(
		t.Context(), store, session.root, []string{"threads"}, 1, 0, marked,
	)
	if err == nil || count != 1 || len(marked) != 0 || !strings.Contains(err.Error(), "referenced blobs exceed 0") {
		t.Fatalf("zero digest budget = count %d marked %d err %v", count, len(marked), err)
	}
}

func TestAttachmentGarbageCollectionRevalidatesAuthorityAfterCandidateHash(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	now := time.Now().UTC()
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "orphan.txt")
	if err := os.WriteFile(source, []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphan, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{Path: source, At: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveAttachmentRefs(t.Context(), lease, metadata, []string{orphan.Ref}); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	path := attachmentTestBlobPath(store, orphan)
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	hashed := make(chan struct{})
	resume := make(chan struct{})
	resumed := false
	t.Cleanup(func() {
		if !resumed {
			close(resume)
		}
	})
	store.afterAttachmentGCCommitValidation = func() {
		close(hashed)
		<-resume
	}
	type collection struct {
		result AttachmentGCResult
		err    error
	}
	completed := make(chan collection, 1)
	go func() {
		result, collectErr := store.CollectAttachmentGarbage(t.Context(), AttachmentGCOptions{
			Before: now.Add(-24 * time.Hour), Delete: true,
		})
		completed <- collection{result: result, err: collectErr}
	}()
	select {
	case <-hashed:
	case <-time.After(10 * time.Second):
		t.Fatal("garbage collector did not reach the pre-delete barrier")
	}
	root, err := os.OpenRoot(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	replacedName := attachmentMaintenanceDirectory + "-replaced"
	if err := root.Rename(attachmentMaintenanceDirectory, replacedName); err != nil {
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
	replacementStore, err := NewStore(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	replacementLease, err := replacementStore.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	replacementAttachment, err := replacementStore.AdmitAttachment(
		t.Context(),
		replacementLease,
		metadata,
		AttachmentInput{Path: source, At: now.Add(time.Minute)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if replacementAttachment.SHA256 != orphan.SHA256 || replacementAttachment.Ref == orphan.Ref {
		t.Fatalf("replacement attachment = %+v, original = %+v", replacementAttachment, orphan)
	}
	if err := replacementLease.Release(); err != nil {
		t.Fatal(err)
	}
	close(resume)
	resumed = true
	var outcome collection
	select {
	case outcome = <-completed:
	case <-time.After(10 * time.Second):
		t.Fatal("garbage collector did not leave the pre-delete barrier")
	}
	if outcome.err == nil || outcome.result.DeletedBlobs != 0 ||
		IsCommittedAttachmentGCError(outcome.err) ||
		!strings.Contains(outcome.err.Error(), "lock directory") {
		t.Fatalf("maintenance-replacement sweep = %+v, %v", outcome.result, outcome.err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("candidate removed after maintenance replacement: %v", err)
	}
	data, _, err := replacementStore.ResolveAttachment(
		t.Context(), metadata.ThreadID, replacementAttachment.Ref,
	)
	if err != nil || string(data) != "orphan" {
		t.Fatalf("replacement reference after sweep = %q, %v", data, err)
	}
}

func TestAttachmentGarbageCollectionRecoversInterruptedQuarantineAfterRestart(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	now := time.Now().UTC()
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	attachment := admitGCTestAttachment(t, store, lease, metadata, "restart.txt", "restart payload", now)
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	shardRoot := filepath.Join("blobs", "sha256", attachment.SHA256[:2])
	quarantineName := attachmentGCQuarantineName(attachment.SHA256)
	if err := root.Link(
		filepath.Join(shardRoot, attachment.SHA256),
		filepath.Join(shardRoot, quarantineName),
	); err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	if err := root.Remove(filepath.Join(shardRoot, attachment.SHA256)); err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ResolveAttachment(
		t.Context(),
		metadata.ThreadID,
		attachment.Ref,
	); !IsAttachmentUnavailable(
		err,
	) {
		t.Fatalf("detached attachment resolution error = %v", err)
	}

	restarted, err := NewStore(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	result, err := restarted.CollectAttachmentGarbage(t.Context(), AttachmentGCOptions{
		Before: now.Add(-24 * time.Hour),
	})
	if err != nil || len(result.Candidates) != 0 || result.DeletedBlobs != 0 {
		t.Fatalf("restart recovery result = %+v, %v", result, err)
	}
	data, _, err := restarted.ResolveAttachment(t.Context(), metadata.ThreadID, attachment.Ref)
	if err != nil || string(data) != "restart payload" {
		t.Fatalf("recovered attachment = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(store.Root(), shardRoot, quarantineName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered quarantine entry remains: %v", err)
	}
}

func admitGCTestAttachment(
	t *testing.T,
	store *Store,
	lease *Lease,
	metadata Metadata,
	filename string,
	content string,
	now time.Time,
) Attachment {
	t.Helper()
	path := filepath.Join(t.TempDir(), filename)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: path, At: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return attachment
}
