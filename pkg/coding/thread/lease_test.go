package thread

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestThreadLeaseIsExclusiveAndReportsOwner(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	wantOwner := LeaseOwner{
		SchemaVersion: LeaseSchemaVersion,
		PID:           4242,
		Hostname:      "fixture-host",
		AcquiredAt:    time.Date(2026, time.August, 10, 9, 30, 0, 0, time.UTC),
	}
	lease, err := store.acquireLease(metadata.ThreadID, wantOwner)
	if err != nil {
		t.Fatalf("acquireLease(first) error = %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	if lease.ThreadID() != metadata.ThreadID || !reflect.DeepEqual(lease.Owner(), wantOwner) {
		t.Fatalf("lease identity = %q / %#v", lease.ThreadID(), lease.Owner())
	}

	contender, err := store.AcquireLease(metadata.ThreadID)
	if contender != nil {
		_ = contender.Release()
		t.Fatal("AcquireLease(contender) returned a lease")
	}
	var busy *LeaseBusyError
	if !errors.Is(err, ErrLeaseBusy) || !errors.As(err, &busy) {
		t.Fatalf("AcquireLease(contender) error = %v, want busy classification", err)
	}
	if busy.ThreadID != metadata.ThreadID || busy.Owner == nil || !reflect.DeepEqual(*busy.Owner, wantOwner) {
		t.Fatalf("busy diagnostic = %#v, want owner %#v", busy, wantOwner)
	}
	if !strings.Contains(err.Error(), "owner pid 4242 on fixture-host") {
		t.Fatalf("busy error lacks bounded owner diagnostic: %v", err)
	}
	inspection, err := store.InspectLease(metadata.ThreadID)
	if err != nil || !inspection.Busy || inspection.Owner == nil ||
		!reflect.DeepEqual(*inspection.Owner, wantOwner) {
		t.Fatalf("busy inspection = %#v, %v", inspection, err)
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	inspection, err = store.InspectLease(metadata.ThreadID)
	if err != nil || inspection.Busy || inspection.Owner != nil {
		t.Fatalf("available inspection = %#v, %v", inspection, err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("second Release() error = %v", err)
	}
	reacquired, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatalf("AcquireLease(after release) error = %v", err)
	}
	if err := reacquired.Release(); err != nil {
		t.Fatalf("release reacquired lease: %v", err)
	}
}

func TestThreadLeaseOverwritesStaleOwnerAndDoesNotBlockCatalog(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lockPath := filepath.Join(store.Root(), "threads", metadata.ThreadID, leaseFileName)
	if err := os.WriteFile(lockPath, []byte("stale malformed owner"), 0o600); err != nil {
		t.Fatalf("WriteFile(stale owner) error = %v", err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatalf("AcquireLease() error = %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })

	catalog, err := NewCatalog(store, CatalogOptions{})
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	page, err := catalog.Query(
		context.Background(),
		CatalogQuery{ProjectKey: metadata.Project.ProjectKey},
	)
	if err != nil {
		t.Fatalf("Query(leased thread) error = %v", err)
	}
	if len(page.Threads) != 1 || page.Threads[0].ThreadID != metadata.ThreadID {
		t.Fatalf("Query(leased thread) = %#v", page)
	}

	file, err := os.Open(lockPath)
	if err != nil {
		t.Fatalf("Open(thread.lock) error = %v", err)
	}
	owner, err := readLeaseOwner(file)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("readLeaseOwner() errors = %v / %v", err, closeErr)
	}
	if !reflect.DeepEqual(*owner, lease.Owner()) {
		t.Fatalf("persisted owner = %#v, want %#v", *owner, lease.Owner())
	}
}

func TestThreadLeaseRecoversAfterOwnerProcessCrash(t *testing.T) {
	const (
		helperRootEnv = "MINTCLAW_TEST_THREAD_LEASE_ROOT"
		helperIDEnv   = "MINTCLAW_TEST_THREAD_LEASE_ID"
	)
	if root := os.Getenv(helperRootEnv); root != "" {
		store, err := NewStore(root)
		if err != nil {
			t.Fatal(err)
		}
		lease, err := store.AcquireLease(os.Getenv(helperIDEnv))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lease.Release() }()
		_, _ = fmt.Fprintln(os.Stdout, "locked")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}

	store, metadata := newLeaseTestThread(t)
	command := exec.Command(os.Args[0], "-test.run=^TestThreadLeaseRecoversAfterOwnerProcessCrash$")
	command.Env = append(
		os.Environ(),
		helperRootEnv+"="+store.Root(),
		helperIDEnv+"="+metadata.ThreadID,
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	t.Cleanup(func() {
		_ = stdin.Close()
		if !finished {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "locked" {
		_ = command.Process.Kill()
		_ = command.Wait()
		finished = true
		t.Fatalf("helper did not acquire thread lease: %s", stderr.String())
	}

	contender, err := store.AcquireLease(metadata.ThreadID)
	if contender != nil {
		_ = contender.Release()
		t.Fatal("AcquireLease(contender) returned a lease")
	}
	var busy *LeaseBusyError
	if !errors.As(err, &busy) || busy.Owner == nil || busy.Owner.PID != command.Process.Pid {
		t.Fatalf("cross-process busy error = %#v / %v", busy, err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed helper exited successfully")
	}
	finished = true

	successor, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatalf("AcquireLease(after crash) error = %v", err)
	}
	if successor.Owner().PID != os.Getpid() {
		t.Fatalf("successor owner = %#v", successor.Owner())
	}
	if err := successor.Release(); err != nil {
		t.Fatalf("release successor: %v", err)
	}
}

func TestThreadLeaseRejectsMissingThreadAndInvalidOwner(t *testing.T) {
	store, _ := newLeaseTestThread(t)
	if _, err := store.AcquireLease(uuid.NewString()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("AcquireLease(missing) error = %v, want not-exist", err)
	}
	invalidOwners := []LeaseOwner{
		{},
		{SchemaVersion: LeaseSchemaVersion, PID: -1, AcquiredAt: time.Now().UTC()},
		{SchemaVersion: LeaseSchemaVersion, PID: 1, Hostname: " padded ", AcquiredAt: time.Now().UTC()},
		{SchemaVersion: LeaseSchemaVersion, PID: 1, Hostname: "line\nbreak", AcquiredAt: time.Now().UTC()},
	}
	for _, owner := range invalidOwners {
		if _, err := store.acquireLease(uuid.NewString(), owner); err == nil {
			t.Fatalf("acquireLease(%#v) succeeded", owner)
		}
	}
}

func TestStoreValidateLeaseRequiresActiveExactOwner(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateLease(lease, metadata.ThreadID); err != nil {
		t.Fatalf("ValidateLease(active) error = %v", err)
	}
	if err := store.ValidateLease(lease, uuid.NewString()); err == nil {
		t.Fatal("ValidateLease(wrong thread) succeeded")
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateLease(lease, metadata.ThreadID); err == nil {
		t.Fatal("ValidateLease(released) succeeded")
	}
}

func newLeaseTestThread(t *testing.T) (*Store, Metadata) {
	t.Helper()
	project, err := ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("ResolveProject() error = %v", err)
	}
	metadata, err := NewMetadata(uuid.NewString(), project, "test thread lease", time.Now().UTC())
	if err != nil {
		t.Fatalf("NewMetadata() error = %v", err)
	}
	store, err := NewStore(filepath.Join(t.TempDir(), "coding"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	return store, metadata
}
