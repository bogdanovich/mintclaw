//go:build windows

package thread

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsThreadTrashRenameAcrossParentsNeverReplacesDestination(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"active", "trash", "active/source", "trash/existing"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	pinned, err := openPinnedCatalogRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	if err := renameThreadReservationNoReplace(
		pinned,
		"active/source",
		"trash/existing",
	); !errors.Is(
		err,
		fs.ErrExist,
	) {
		t.Fatalf("existing destination rename error = %v, want fs.ErrExist", err)
	}
	if err := renameThreadReservationNoReplace(pinned, "active/source", "trash/moved"); err != nil {
		t.Fatalf("cross-parent directory rename: %v", err)
	}
	if _, err := pinned.Stat("trash/moved"); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsThreadTrashRenameGateRetiresWriterWithoutOpeningAdmission(t *testing.T) {
	root := t.TempDir()
	project, _ := purgeTestProject(t, root, "project")
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := NewMetadata(NewThreadID(), project, "Windows writer handoff", time.Now())
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
	defer func() { _ = lease.Release() }()
	original := store.renameReservation
	store.renameReservation = func(root *os.Root, oldName, newName string) error {
		if !lease.released {
			t.Fatal("cross-directory move retained an open writer file")
		}
		catalogRoot, err := openCatalogRoot(store.Root())
		if err != nil {
			return err
		}
		contender, openErr := openLeaseFile(catalogRoot, catalogLeaseFileName)
		if err := errors.Join(openErr, catalogRoot.Close()); err != nil {
			return err
		}
		lockErr := tryAcquireThreadLeaseFile(contender)
		if lockErr == nil {
			_ = releaseThreadLeaseFile(contender)
		}
		closeErr := contender.Close()
		if !errors.Is(lockErr, ErrLeaseBusy) {
			t.Fatalf("writer admission gate error = %v, want ErrLeaseBusy", lockErr)
		}
		if closeErr != nil {
			return closeErr
		}
		return original(root, oldName, newName)
	}
	if _, err := store.TrashThread(lease, metadata.ThreadID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireLease(metadata.ThreadID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("writer adopted a removed active path: %v", err)
	}
}

func TestWindowsThreadTrashFailedPublicationAllowsFreshLeaseRetry(t *testing.T) {
	root := t.TempDir()
	project, _ := purgeTestProject(t, root, "project")
	store, err := NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := NewMetadata(NewThreadID(), project, "Windows retry", time.Now())
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
	original := store.renameReservation
	store.renameReservation = func(*os.Root, string, string) error { return errors.New("fixture before publication") }
	if result, err := store.TrashThread(lease, metadata.ThreadID, time.Now()); err == nil ||
		IsCommittedTrashError(err) || result.TrashID != "" || !lease.released {
		t.Fatalf("failed publication result = %+v, error = %v, retired = %t", result, err, lease.released)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	store.renameReservation = original
	fresh, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatalf("fresh writer admission remained blocked: %v", err)
	}
	defer func() { _ = fresh.Release() }()
	if _, err := store.TrashThread(fresh, metadata.ThreadID, time.Now()); err != nil {
		t.Fatalf("explicit fresh-lease retry: %v", err)
	}
}
