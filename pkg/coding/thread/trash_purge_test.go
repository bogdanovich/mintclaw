package thread

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPurgeThreadTrashPlansThenDeletesOnlyExpiredOwnedState(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	firstProject, firstSentinel := purgeTestProject(t, root, "first-project")
	secondProject, secondSentinel := purgeTestProject(t, root, "second-project")
	now := time.Now().UTC().Truncate(time.Second)
	old := trashThreadFixture(t, store, firstProject, now.Add(-45*24*time.Hour))
	for entry := range ownedThreadEntries {
		if entry == metadataFileName || entry == leaseFileName {
			continue
		}
		directory := filepath.Join(old.Path, entry)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "private-derivative"), []byte("private\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	recent := trashThreadFixture(t, store, secondProject, now.Add(-2*24*time.Hour))
	active, err := NewMetadata(NewThreadID(), firstProject, "active", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(active); err != nil {
		t.Fatal(err)
	}
	blobSentinel := filepath.Join(store.Root(), "blobs", "shared-sentinel")
	if err := os.MkdirAll(filepath.Dir(blobSentinel), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blobSentinel, []byte("shared\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	options := ThreadTrashPurgeOptions{Before: now.Add(-30 * 24 * time.Hour)}
	planned, err := store.PurgeThreadTrash(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if planned.Scanned != 2 || planned.Deleted != 0 || len(planned.Candidates) != 1 ||
		planned.Candidates[0].TrashID != old.TrashID {
		t.Fatalf("plan = %+v", planned)
	}
	for _, path := range []string{old.Path, recent.Path} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("dry run changed %q: %v", path, err)
		}
	}

	options.Delete = true
	purged, err := store.PurgeThreadTrash(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if purged.Deleted != 1 || len(purged.DeletedTrashIDs) != 1 ||
		purged.DeletedTrashIDs[0] != old.TrashID {
		t.Fatalf("purge = %+v", purged)
	}
	if _, err := os.Stat(old.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired trash remains: %v", err)
	}
	for _, path := range []string{recent.Path, firstSentinel, secondSentinel, blobSentinel} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("out-of-scope path %q changed: %v", path, err)
		}
	}
	if _, err := store.Load(active.ThreadID); err != nil {
		t.Fatalf("active thread changed: %v", err)
	}
}

func TestPurgeThreadTrashFailsClosedForUnknownAndLinkedState(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, TrashResult, string)
		want   string
	}{
		{
			name: "unknown owned entry",
			mutate: func(t *testing.T, trashed TrashResult, _ string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(trashed.Path, "unknown-private-copy"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "cannot confirm ownership",
		},
		{
			name: "linked owned entry",
			mutate: func(t *testing.T, trashed TrashResult, outside string) {
				t.Helper()
				if err := os.Symlink(outside, filepath.Join(trashed.Path, "media")); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
			want: "symbolic-link",
		},
		{
			name: "linked trash root",
			mutate: func(t *testing.T, trashed TrashResult, outside string) {
				t.Helper()
				moved := filepath.Join(outside, "recoverable-thread")
				if err := os.Rename(trashed.Path, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, trashed.Path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
			want: "not a direct directory",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewStore(filepath.Join(root, "coding"))
			if err != nil {
				t.Fatal(err)
			}
			project, sentinel := purgeTestProject(t, root, "project")
			outside := filepath.Join(root, "outside")
			if err := os.Mkdir(outside, 0o700); err != nil {
				t.Fatal(err)
			}
			trashed := trashThreadFixture(t, store, project, time.Now().Add(-40*24*time.Hour))
			test.mutate(t, trashed, outside)
			_, err = store.PurgeThreadTrash(t.Context(), ThreadTrashPurgeOptions{
				Before: time.Now().Add(-30 * 24 * time.Hour),
				Delete: true,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("PurgeThreadTrash() error = %v, want %q", err, test.want)
			}
			if _, err := os.Stat(sentinel); err != nil {
				t.Fatalf("project sentinel changed: %v", err)
			}
		})
	}
}

func TestPurgeThreadTrashPreservesSharedBlobUntilLastRecoverableReferenceIsPurged(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project, _ := purgeTestProject(t, root, "project")
	source := filepath.Join(root, "private-attachment.txt")
	if err := os.WriteFile(source, []byte("shared private bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var attachment Attachment
	for _, age := range []time.Duration{45 * 24 * time.Hour, 2 * 24 * time.Hour} {
		metadata, err := NewMetadata(NewThreadID(), project, "attachment owner", now.Add(-age-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Save(metadata); err != nil {
			t.Fatal(err)
		}
		lease, err := store.AcquireLease(metadata.ThreadID)
		if err != nil {
			t.Fatal(err)
		}
		attachment, err = store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{Path: source, At: now})
		if err != nil {
			_ = lease.Release()
			t.Fatal(err)
		}
		_, trashErr := store.TrashThread(lease, metadata.ThreadID, now.Add(-age))
		if err := errors.Join(trashErr, lease.Release()); err != nil {
			t.Fatal(err)
		}
	}
	blob := filepath.Join(store.Root(), "blobs", "sha256", attachment.SHA256[:2], attachment.SHA256)
	old := now.Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(blob, old, old); err != nil {
		t.Fatal(err)
	}
	for index, cutoff := range []time.Time{now.Add(-30 * 24 * time.Hour), now.Add(-24 * time.Hour)} {
		purged, err := store.PurgeThreadTrash(t.Context(), ThreadTrashPurgeOptions{Before: cutoff, Delete: true})
		if err != nil || purged.Deleted != 1 {
			t.Fatalf("purge stage %d: %+v, %v", index, purged, err)
		}
		collected, err := store.CollectAttachmentGarbage(t.Context(), AttachmentGCOptions{
			Before: now.Add(-24 * time.Hour), Delete: true,
		})
		if err != nil || collected.DeletedBlobs != index {
			t.Fatalf("GC stage %d: %+v, %v", index, collected, err)
		}
	}
	if _, err := os.Stat(blob); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("last unreferenced blob retained: %v", err)
	}
}

func TestPurgeThreadTrashEnforcesBoundedScanAndMaintenanceLease(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project, _ := purgeTestProject(t, root, "project")
	now := time.Now().UTC()
	for range 2 {
		trashThreadFixture(t, store, project, now.Add(-40*24*time.Hour))
	}
	options := ThreadTrashPurgeOptions{Before: now.Add(-30 * 24 * time.Hour), EntryLimit: 1}
	if _, err := store.PurgeThreadTrash(t.Context(), options); err == nil ||
		!strings.Contains(err.Error(), "exceeds 1 entries") {
		t.Fatalf("bounded scan error = %v", err)
	}

	maintenance, err := store.acquireAttachmentMaintenanceLease()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = maintenance.Release() }()
	options.EntryLimit = 0
	options.Delete = true
	if _, err := store.PurgeThreadTrash(t.Context(), options); !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf("concurrent purge error = %v, want ErrLeaseBusy", err)
	}
	options.Delete = false
	if planned, err := store.PurgeThreadTrash(t.Context(), options); err != nil || len(planned.Candidates) != 2 {
		t.Fatalf("dry run under maintenance lease = %+v, %v", planned, err)
	}
}

func TestPurgeThreadTrashDoesNotFollowNestedSymlinks(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project, sentinel := purgeTestProject(t, root, "project")
	now := time.Now().UTC()
	trashed := trashThreadFixture(t, store, project, now.Add(-40*24*time.Hour))
	mediaRoot := filepath.Join(trashed.Path, "media")
	if err := os.Mkdir(mediaRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(project.ProjectRoot, filepath.Join(mediaRoot, "nested-link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	result, err := store.PurgeThreadTrash(t.Context(), ThreadTrashPurgeOptions{
		Before: now.Add(-30 * 24 * time.Hour),
		Delete: true,
	})
	if err != nil || result.Deleted != 1 {
		t.Fatalf("nested link result = %+v, error = %v", result, err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep\n" {
		t.Fatalf("nested link affected project: %q, %v", data, err)
	}
}

func TestPurgeThreadTrashReportsCommittedDurabilityFailure(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project, _ := purgeTestProject(t, root, "project")
	now := time.Now().UTC()
	trashed := trashThreadFixture(t, store, project, now.Add(-40*24*time.Hour))
	store.syncRoot = func(*os.Root) error { return errors.New("fixture sync failure") }
	result, err := store.PurgeThreadTrash(t.Context(), ThreadTrashPurgeOptions{
		Before: now.Add(-30 * 24 * time.Hour),
		Delete: true,
	})
	if !IsCommittedThreadTrashPurgeError(err) || result.Deleted != 1 ||
		len(result.DeletedTrashIDs) != 1 || result.DeletedTrashIDs[0] != trashed.TrashID {
		t.Fatalf("committed result = %+v, error = %v", result, err)
	}
	if _, statErr := os.Stat(trashed.Path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("committed deletion was not visible: %v", statErr)
	}
}

func TestPurgeThreadTrashReportsPartialRecursiveFailureBeforeAnyRootCompletes(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project, sentinel := purgeTestProject(t, root, "project")
	now := time.Now().UTC()
	trashed := trashThreadFixture(t, store, project, now.Add(-40*24*time.Hour))
	store.removeThreadTrash = func(root *os.Root, name string) error {
		if err := root.Remove(filepath.Join(name, metadataFileName)); err != nil {
			return err
		}
		return errors.New("fixture partial recursive failure")
	}
	result, err := store.PurgeThreadTrash(t.Context(), ThreadTrashPurgeOptions{
		Before: now.Add(-30 * 24 * time.Hour),
		Delete: true,
	})
	if !IsCommittedThreadTrashPurgeError(err) || result.Deleted != 0 ||
		result.IncompleteTrashID != trashed.TrashID || len(result.AttemptedTrashIDs) != 1 {
		t.Fatalf("partial recursive result = %+v, error = %v", result, err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("partial deletion touched project: %v", err)
	}
}

func TestPurgeThreadTrashRejectsCandidateReplacementAfterPlanning(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project, _ := purgeTestProject(t, root, "project")
	now := time.Now().UTC()
	trashed := trashThreadFixture(t, store, project, now.Add(-40*24*time.Hour))
	maintenance, err := store.acquireAttachmentMaintenanceLease()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = maintenance.Release() }()
	planned, err := store.planThreadTrashPurge(t.Context(), maintenance.root, ThreadTrashPurgeOptions{
		Before:     now.Add(-30 * 24 * time.Hour),
		EntryLimit: DefaultThreadTrashScanLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(trashed.Path, filepath.Join(root, "retained-original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(trashed.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := store.deleteThreadTrashCandidates(t.Context(), maintenance, planned)
	if err == nil || !strings.Contains(err.Error(), "changed after planning") ||
		len(result.AttemptedTrashIDs) != 0 || IsCommittedThreadTrashPurgeError(err) {
		t.Fatalf("replacement result = %+v, error = %v", result, err)
	}
	if _, err := os.Stat(trashed.Path); err != nil {
		t.Fatalf("replacement root was removed: %v", err)
	}
}

func TestPurgeThreadTrashIsSideEffectFreeBeforeStoreExists(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing", "coding")
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, deleting := range []bool{false, true} {
		result, err := store.PurgeThreadTrash(t.Context(), ThreadTrashPurgeOptions{
			Before: time.Now().Add(-30 * 24 * time.Hour),
			Delete: deleting,
		})
		if err != nil || result.Scanned != 0 || result.Candidates == nil || result.Deleted != 0 {
			t.Fatalf("missing store deleting=%t: result=%+v error=%v", deleting, result, err)
		}
		if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing store materialized: %v", err)
		}
	}
}

func purgeTestProject(t *testing.T, root, name string) (ProjectIdentity, string) {
	t.Helper()
	projectRoot := filepath.Join(root, name)
	if err := os.Mkdir(projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(projectRoot, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	return project, sentinel
}

func trashThreadFixture(t *testing.T, store *Store, project ProjectIdentity, at time.Time) TrashResult {
	t.Helper()
	metadata, err := NewMetadata(NewThreadID(), project, "purge fixture", at.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	result, trashErr := store.TrashThread(lease, metadata.ThreadID, at)
	releaseErr := lease.Release()
	if err := errors.Join(trashErr, releaseErr); err != nil {
		t.Fatal(err)
	}
	return result
}
