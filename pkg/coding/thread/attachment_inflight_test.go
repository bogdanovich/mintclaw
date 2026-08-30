package thread

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAttachmentInflightRecoveryRemovesCrashStaleMarker(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := store.openAttachmentStoreView(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("crash-stale marker")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	batch, err := view.beginAttachmentInflight([]preparedAttachment{{
		attachment: Attachment{SHA256: digest},
	}})
	if err != nil {
		t.Fatal(err)
	}
	marker := attachmentInflightMarker{
		Version: attachmentInflightVersion, ThreadID: metadata.ThreadID, Digest: digest, BatchID: batch.batchID,
	}
	markerPath := attachmentInflightMarkerPath(store.Root(), marker)
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewStore(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := restarted.CollectAttachmentGarbage(t.Context(), AttachmentGCOptions{
		Before: time.Now().Add(-24 * time.Hour),
	})
	if err != nil || plan.ScannedManifests != 1 || plan.ReferencedBlobs != 0 || plan.DeletedBlobs != 0 {
		t.Fatalf("crash-stale marker plan = %+v, %v", plan, err)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("dry-run removed crash-stale marker: %v", err)
	}
	result, err := restarted.CollectAttachmentGarbage(t.Context(), AttachmentGCOptions{
		Before: time.Now().Add(-24 * time.Hour), Delete: true,
	})
	if err != nil || result.ScannedManifests != 1 || result.ReferencedBlobs != 0 || result.DeletedBlobs != 0 {
		t.Fatalf("crash-stale marker recovery = %+v, %v", result, err)
	}
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crash-stale marker remains: %v", err)
	}
}

func TestAttachmentInflightMarkerRejectsPathAndContentReplacement(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	view, err := store.openAttachmentStoreView(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = view.Close() }()
	content := []byte("marker identity")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	batch, err := view.beginAttachmentInflight([]preparedAttachment{{
		attachment: Attachment{SHA256: digest},
	}})
	if err != nil {
		t.Fatal(err)
	}
	marker := attachmentInflightMarker{
		Version: attachmentInflightVersion, ThreadID: metadata.ThreadID, Digest: digest, BatchID: batch.batchID,
	}
	markerPath := attachmentInflightMarkerPath(store.Root(), marker)
	if err := os.WriteFile(markerPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := batch.Remove(); err == nil {
		t.Fatal("replaced marker was accepted for cleanup")
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("replaced marker was removed: %v", err)
	}

	replacement := filepath.Join(t.TempDir(), "marker.json")
	if err := os.WriteFile(replacement, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacement, markerPath); err != nil {
		t.Skipf("symbolic links unavailable on this platform: %v", err)
	}
	if _, err := readAttachmentInflightMarkerPathForTest(markerPath); err == nil {
		t.Fatal("symbolic-link marker was accepted")
	}
}

func TestAttachmentInflightGarbageCollectionFailsClosedOnCorruptMarker(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	view, err := store.openAttachmentStoreView(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = view.Close() }()
	sum := sha256.Sum256([]byte("corrupt marker"))
	digest := hex.EncodeToString(sum[:])
	batch, err := view.beginAttachmentInflight([]preparedAttachment{{
		attachment: Attachment{SHA256: digest},
	}})
	if err != nil {
		t.Fatal(err)
	}
	marker := attachmentInflightMarker{
		Version: attachmentInflightVersion, ThreadID: metadata.ThreadID, Digest: digest, BatchID: batch.batchID,
	}
	if err := os.WriteFile(
		attachmentInflightMarkerPath(store.Root(), marker),
		[]byte("{corrupt\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	result, err := store.CollectAttachmentGarbage(t.Context(), AttachmentGCOptions{
		Before: time.Now().Add(-24 * time.Hour), Delete: true,
	})
	if err == nil || result.DeletedBlobs != 0 || IsCommittedAttachmentGCError(err) {
		t.Fatalf("corrupt in-flight marker sweep = %+v, %v", result, err)
	}
}

func TestAttachmentInflightGarbageCollectionEnforcesMarkerBudget(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	view, err := store.openAttachmentStoreView(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = view.Close() }()
	sum := sha256.Sum256([]byte("bounded marker"))
	digest := hex.EncodeToString(sum[:])
	if _, err := view.beginAttachmentInflight([]preparedAttachment{{
		attachment: Attachment{SHA256: digest},
	}}); err != nil {
		t.Fatal(err)
	}
	session, err := store.acquireAttachmentGCSession()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	count, err := store.scanAttachmentInflight(
		t.Context(), session.root, true, 0, attachmentGCMarkedBlobLimit, map[string]struct{}{},
	)
	if err == nil || count != 0 {
		t.Fatalf("zero in-flight marker budget = %d, %v", count, err)
	}
}

func readAttachmentInflightMarkerPathForTest(path string) (attachmentInflightMarker, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return attachmentInflightMarker{}, err
	}
	defer func() { _ = root.Close() }()
	return readAttachmentInflightMarker(root, filepath.Base(path))
}

func attachmentInflightMarkerPath(storeRoot string, marker attachmentInflightMarker) string {
	return filepath.Join(
		storeRoot,
		attachmentInflightDirectory,
		attachmentInflightSHA256Directory,
		marker.Digest[:2],
		marker.Digest,
		attachmentInflightMarkerName(marker.BatchID),
	)
}
