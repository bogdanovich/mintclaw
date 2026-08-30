package thread

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	if string(resolved) != "bounded report\n" {
		t.Fatalf("resolved changed-source content = %q", resolved)
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
	if string(resolved) != "bounded report\n" {
		t.Fatalf("resolved content = %q", resolved)
	}
	manifest, err := os.ReadFile(attachmentTestManifestPath(t, store, metadata.ThreadID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifest), secretDirectory) {
		t.Fatalf("attachment manifest disclosed source directory: %s", manifest)
	}
}

func TestAttachmentBatchCommitsAllReferencesAtomically(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	sourceRoot := t.TempDir()
	firstPath := filepath.Join(sourceRoot, "first.log")
	secondPath := filepath.Join(sourceRoot, "second.log")
	if err := os.WriteFile(firstPath, []byte("shared bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("shared bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	attachments, err := store.AdmitAttachments(t.Context(), lease, metadata, []AttachmentInput{
		{Path: firstPath, At: now},
		{Path: secondPath, At: now.Add(time.Second)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 2 || attachments[0].Ref == attachments[1].Ref ||
		attachments[0].SHA256 != attachments[1].SHA256 {
		t.Fatalf("batch attachments = %+v", attachments)
	}
	entries, err := store.ListAttachments(metadata.ThreadID)
	if err != nil || len(entries) != 2 {
		t.Fatalf("manifest entries = %+v, %v", entries, err)
	}
	for _, attachment := range attachments {
		data, _, resolveErr := store.ResolveAttachment(t.Context(), metadata.ThreadID, attachment.Ref)
		if resolveErr != nil || string(data) != "shared bytes" {
			t.Fatalf("resolve %q = %q, %v", attachment.Ref, data, resolveErr)
		}
	}
}

func TestAttachmentBatchDoesNotPublishPartialManifest(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	validPath := filepath.Join(t.TempDir(), "valid.log")
	if err := os.WriteFile(validPath, []byte("valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseline, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: validPath, At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := store.AdmitAttachments(t.Context(), lease, metadata, []AttachmentInput{
		{Path: validPath, At: time.Now()},
		{Path: filepath.Join(t.TempDir(), "missing.log"), At: time.Now()},
	})
	if err == nil || len(attachments) != 0 {
		t.Fatalf("partial batch = %+v, %v", attachments, err)
	}
	entries, listErr := store.ListAttachments(metadata.ThreadID)
	if listErr != nil || len(entries) != 1 || entries[0].Ref != baseline.Ref {
		t.Fatalf("manifest changed after partial batch = %+v, %v", entries, listErr)
	}
}

func TestAttachmentBatchRejectsUnboundedCountBeforeReading(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	inputs := make([]AttachmentInput, MaxAttachmentAdmissionCount+1)
	for index := range inputs {
		inputs[index] = AttachmentInput{Path: "/missing", At: time.Now()}
	}
	attachments, err := store.AdmitAttachments(t.Context(), lease, metadata, inputs)
	if err == nil || len(attachments) != 0 || !strings.Contains(err.Error(), "batch must contain") {
		t.Fatalf("unbounded batch = %+v, %v", attachments, err)
	}
}

func TestAttachmentBatchBoundsAggregateBytesWithoutOverflow(t *testing.T) {
	total, err := addAttachmentAdmissionBytes(MaxAttachmentAdmissionBytes-1, 1)
	if err != nil || total != MaxAttachmentAdmissionBytes {
		t.Fatalf("exact aggregate bound = %d, %v", total, err)
	}
	for _, values := range [][2]int64{
		{MaxAttachmentAdmissionBytes, 1},
		{MaxAttachmentAdmissionBytes + 1, 0},
		{0, -1},
	} {
		if _, err := addAttachmentAdmissionBytes(values[0], values[1]); err == nil {
			t.Fatalf("aggregate bytes %v accepted", values)
		}
	}
}

func TestAttachmentRemovalDropsExactRefsWithoutDeletingBlobs(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	sourceRoot := t.TempDir()
	paths := []string{
		filepath.Join(sourceRoot, "keep.txt"),
		filepath.Join(sourceRoot, "remove-one.txt"),
		filepath.Join(sourceRoot, "remove-two.txt"),
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("shared"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	attachments, err := store.AdmitAttachments(t.Context(), lease, metadata, []AttachmentInput{
		{Path: paths[0], At: time.Now()},
		{Path: paths[1], At: time.Now()},
		{Path: paths[2], At: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	blobPath := attachmentTestBlobPath(store, attachments[0])
	if err := store.RemoveAttachmentRefs(t.Context(), lease, metadata, []string{
		attachments[1].Ref,
		attachments[2].Ref,
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListAttachments(metadata.ThreadID)
	if err != nil || len(entries) != 1 || entries[0].Ref != attachments[0].Ref {
		t.Fatalf("remaining manifest = %+v, %v", entries, err)
	}
	if _, err := os.Stat(blobPath); err != nil {
		t.Fatalf("shared blob removed with refs: %v", err)
	}
	if _, _, err := store.ResolveAttachment(
		t.Context(), metadata.ThreadID, attachments[1].Ref,
	); !IsAttachmentUnavailable(err) {
		t.Fatalf("removed ref resolution error = %v", err)
	}
	data, _, err := store.ResolveAttachment(t.Context(), metadata.ThreadID, attachments[0].Ref)
	if err != nil || string(data) != "shared" {
		t.Fatalf("remaining ref = %q, %v", data, err)
	}
}

func TestAttachmentAdmissionNormalizesSuppliedMediaType(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	source := filepath.Join(t.TempDir(), "screen.png")
	if err := os.WriteFile(source, []byte("image fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, ContentType: "Image/PNG; CHARSET=UTF-8", At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if attachment.ContentType != "image/png; charset=UTF-8" {
		t.Fatalf("normalized content type = %q", attachment.ContentType)
	}
}

func TestAttachmentLeaseBoundResolveRejectsReplacedThread(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	source := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(source, []byte("owned bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	threadRoot, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	movedRoot := threadRoot + "-moved"
	if err := os.Rename(threadRoot, movedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(threadRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(threadRoot, leaseFileName), []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ResolveAttachmentWithLease(
		t.Context(), lease, metadata.ThreadID, attachment.Ref,
	); err == nil || !strings.Contains(err.Error(), "held lease") {
		t.Fatalf("lease-bound replaced-thread resolution error = %v", err)
	}
}

func TestAttachmentRemovalRejectsUnknownRefWithoutChangingManifest(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	source := filepath.Join(t.TempDir(), "keep.txt")
	if err := os.WriteFile(source, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	unknown := attachmentRefPrefix + NewThreadID()
	if err := store.RemoveAttachmentRefs(
		t.Context(), lease, metadata, []string{unknown},
	); err == nil {
		t.Fatal("RemoveAttachmentRefs() accepted an unknown reference")
	}
	entries, err := store.ListAttachments(metadata.ThreadID)
	if err != nil || len(entries) != 1 || entries[0].Ref != attachment.Ref {
		t.Fatalf("manifest changed after unknown removal = %+v, %v", entries, err)
	}
}

func TestAttachmentSharesBlobAndSurvivesOtherThreadDeletion(t *testing.T) {
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
	if err != nil || repeated.Ref == "" || repeated.Ref == firstAttachment.Ref ||
		repeated.SHA256 != firstAttachment.SHA256 {
		t.Fatalf("repeated admission = %+v, %v", repeated, err)
	}
	secondLease := acquireAttachmentTestLease(t, store, second.ThreadID)
	secondAttachment, err := store.AdmitAttachment(t.Context(), secondLease, second, AttachmentInput{
		Path: source, At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	firstData, _, err := store.ResolveAttachment(t.Context(), first.ThreadID, firstAttachment.Ref)
	if err != nil {
		t.Fatal(err)
	}
	repeatedData, _, err := store.ResolveAttachment(t.Context(), first.ThreadID, repeated.Ref)
	if err != nil {
		t.Fatal(err)
	}
	secondData, _, err := store.ResolveAttachment(t.Context(), second.ThreadID, secondAttachment.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstData, repeatedData) || !bytes.Equal(firstData, secondData) ||
		firstAttachment.Ref == secondAttachment.Ref || repeated.Ref == secondAttachment.Ref {
		t.Fatalf(
			"data/refs = %q %q %q / %q %q %q",
			firstData,
			repeatedData,
			secondData,
			firstAttachment.Ref,
			repeated.Ref,
			secondAttachment.Ref,
		)
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

func TestAttachmentAdmissionRejectsReplacedThread(t *testing.T) {
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
	if err == nil || attachment.Ref != "" || !strings.Contains(err.Error(), "held lease") {
		t.Fatalf("replacement admission = %+v, %v", attachment, err)
	}
}

func TestListAttachmentsWithLeaseReturnsMetadataAndRejectsReleasedWriter(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	source := filepath.Join(t.TempDir(), "catalog.log")
	if err := os.WriteFile(source, []byte("catalog contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	admitted, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := store.ListAttachmentsWithLease(t.Context(), lease, metadata.ThreadID)
	if err != nil || len(attachments) != 1 || attachments[0] != admitted {
		t.Fatalf("lease-bound list = %+v, %v", attachments, err)
	}
	if err = lease.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ListAttachmentsWithLease(t.Context(), lease, metadata.ThreadID); err == nil ||
		!strings.Contains(err.Error(), "released") {
		t.Fatalf("released lease list error = %v", err)
	}
}

func TestAttachmentReadmissionRepairsMissingBlobAndRejectsCorruption(t *testing.T) {
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
	blobPath := attachmentTestBlobPath(store, attachment)
	if err := os.Remove(blobPath); err != nil {
		t.Fatal(err)
	}
	repaired, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, At: time.Now().Add(time.Minute),
	})
	if err != nil || repaired.Ref == "" || repaired.Ref == attachment.Ref || repaired.SHA256 != attachment.SHA256 {
		t.Fatalf("missing-blob readmission = %+v, %v", repaired, err)
	}
	data, _, err := store.ResolveAttachment(t.Context(), metadata.ThreadID, attachment.Ref)
	if err != nil || string(data) != "content" {
		t.Fatalf("repaired blob = %q, %v", data, err)
	}
	repairedData, _, err := store.ResolveAttachment(t.Context(), metadata.ThreadID, repaired.Ref)
	if err != nil || string(repairedData) != "content" {
		t.Fatalf("new reference to repaired blob = %q, %v", repairedData, err)
	}
	if err := os.WriteFile(blobPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	rejected, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, At: time.Now().Add(2 * time.Minute),
	})
	if err == nil || rejected.Ref != "" || !strings.Contains(err.Error(), "existing digest path is invalid") {
		t.Fatalf("corrupt-blob readmission = %+v, %v", rejected, err)
	}
}

func TestAttachmentAdmissionRequiresExistingBlobDurabilityBarriers(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		target func(*Store, Attachment) string
	}{
		{
			name: "directory parent",
			target: func(store *Store, _ Attachment) string {
				return filepath.Join(store.Root(), "blobs")
			},
		},
		{
			name: "blob shard",
			target: func(store *Store, attachment Attachment) string {
				return filepath.Dir(attachmentTestBlobPath(store, attachment))
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
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
			target := filepath.Clean(testCase.target(store, attachment))
			injected := errors.New("injected existing-blob sync failure")
			syncRoot := store.syncRoot
			store.syncRoot = func(root *os.Root) error {
				if filepath.Clean(root.Name()) == target {
					return injected
				}
				return syncRoot(root)
			}
			repeated, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
				Path: source, At: time.Now().Add(time.Minute),
			})
			if repeated.Ref != "" || !errors.Is(err, injected) {
				t.Fatalf("readmission without durability barrier = %+v, %v", repeated, err)
			}
		})
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
	if string(resolved) != "right" {
		t.Fatalf("resolved whitespace path content = %q", resolved)
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

func TestResolveAttachmentCancellationPrecedesLookupAndManifestErrors(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	for _, testCase := range []struct {
		name  string
		setup func(*testing.T)
	}{
		{name: "unknown reference", setup: func(*testing.T) {}},
		{
			name: "corrupt manifest",
			setup: func(t *testing.T) {
				threadRoot, err := store.ThreadRoot(metadata.ThreadID)
				if err != nil {
					t.Fatal(err)
				}
				directory := filepath.Join(threadRoot, attachmentDirectory)
				if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
					t.Fatal(err)
				}
				if err := os.WriteFile(
					filepath.Join(directory, attachmentManifest),
					[]byte("not-json\n"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.setup(t)
			canceled, cancel := context.WithCancel(t.Context())
			cancel()
			data, attachment, err := store.ResolveAttachment(
				canceled,
				metadata.ThreadID,
				attachmentRefPrefix+NewThreadID(),
			)
			if len(data) != 0 || attachment.Ref != "" || !errors.Is(err, context.Canceled) ||
				IsAttachmentUnavailable(err) {
				t.Fatalf("canceled resolution = %q, %+v, %v", data, attachment, err)
			}
		})
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

func TestAttachmentBatchReturnsEveryCommittedReference(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	sourceRoot := t.TempDir()
	paths := []string{filepath.Join(sourceRoot, "first.txt"), filepath.Join(sourceRoot, "second.txt")}
	for index, path := range paths {
		if err := os.WriteFile(path, []byte(fmt.Sprintf("content-%d", index)), 0o600); err != nil {
			t.Fatal(err)
		}
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
	attachments, err := store.AdmitAttachments(t.Context(), lease, metadata, []AttachmentInput{
		{Path: paths[0], At: time.Now()},
		{Path: paths[1], At: time.Now()},
	})
	if len(attachments) != 2 || !IsCommittedAttachmentsError(err) || !errors.Is(err, injected) {
		t.Fatalf("committed batch = %+v, %v", attachments, err)
	}
	entries, listErr := store.ListAttachments(metadata.ThreadID)
	if listErr != nil || len(entries) != 2 {
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
	if err == nil || attachment.Ref != "" || !strings.Contains(err.Error(), "changed during operation") {
		t.Fatalf("detached blob admission = %+v, %v", attachment, err)
	}
	entries, listErr := store.ListAttachments(metadata.ThreadID)
	if listErr != nil || len(entries) != 0 {
		t.Fatalf("manifest changed after detached blob publication = %+v, %v", entries, listErr)
	}
}

func TestAttachmentAdmissionRejectsSymlinkedBlobHierarchyDuringOpen(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	source := filepath.Join(t.TempDir(), "attachment.txt")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	blobs := filepath.Join(store.Root(), "blobs")
	moved := filepath.Join(store.Root(), "blobs-owned")
	replaced := false
	syncRoot := store.syncRoot
	store.syncRoot = func(root *os.Root) error {
		if err := syncRoot(root); err != nil {
			return err
		}
		if !replaced && filepath.Clean(root.Name()) == filepath.Join(blobs, "sha256") {
			replaced = true
			if err := os.Rename(blobs, moved); err != nil {
				return err
			}
			if err := os.Symlink(filepath.Base(moved), blobs); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
		}
		return nil
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, At: time.Now(),
	})
	if !replaced {
		t.Fatal("blob hierarchy was not replaced")
	}
	if err == nil || attachment.Ref != "" || !strings.Contains(err.Error(), "not direct") {
		t.Fatalf("symlinked blob hierarchy admission = %+v, %v", attachment, err)
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
	if err == nil || attachment.Ref != "" || !strings.Contains(err.Error(), "changed during operation") {
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
	if err == nil || attachment.Ref != "" || !strings.Contains(err.Error(), "changed during operation") {
		t.Fatalf("detached thread admission = %+v, %v", attachment, err)
	}
	entries, listErr := store.ListAttachments(metadata.ThreadID)
	if listErr != nil || len(entries) != 0 {
		t.Fatalf("active manifest changed after detached thread publication = %+v, %v", entries, listErr)
	}
}

func TestResolveAttachmentRejectsReplacedBlobHierarchy(t *testing.T) {
	for _, component := range []string{"blobs", filepath.Join("blobs", "sha256")} {
		t.Run(component, func(t *testing.T) {
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
			active := filepath.Join(store.Root(), component)
			moved := active + "-moved"
			if err := os.Rename(active, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, active); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			data, loaded, err := store.ResolveAttachment(t.Context(), metadata.ThreadID, attachment.Ref)
			if len(data) != 0 || loaded.Ref != attachment.Ref || !IsAttachmentUnavailable(err) ||
				!strings.Contains(err.Error(), "not direct") {
				t.Fatalf("replaced blob hierarchy resolution = %q, %+v, %v", data, loaded, err)
			}
		})
	}
}

func TestPinnedManifestReadRejectsDetachedAttachmentsDirectory(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "present manifest"
		if missing {
			name = "missing manifest"
		}
		t.Run(name, func(t *testing.T) {
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
			view, err := store.openAttachmentStoreView(metadata.ThreadID)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = view.Close() }()
			hierarchy, err := store.openAttachmentHierarchy(view.thread, false, attachmentDirectory)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = hierarchy.Close() }()
			threadRoot, err := store.ThreadRoot(metadata.ThreadID)
			if err != nil {
				t.Fatal(err)
			}
			attachmentsRoot := filepath.Join(threadRoot, attachmentDirectory)
			if missing {
				if err := os.Remove(filepath.Join(attachmentsRoot, attachmentManifest)); err != nil {
					t.Fatal(err)
				}
			}
			moved := attachmentsRoot + "-moved"
			if err := os.Rename(attachmentsRoot, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(attachmentsRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			manifest, err := view.loadManifestFromHierarchy(hierarchy)
			if err == nil || len(manifest.Entries) != 0 || !strings.Contains(err.Error(), "changed during operation") {
				t.Fatalf("detached manifest read = %+v, %v", manifest, err)
			}
		})
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

func attachmentTestBlobPath(store *Store, attachment Attachment) string {
	return filepath.Join(store.Root(), "blobs", "sha256", attachment.SHA256[:2], attachment.SHA256)
}
