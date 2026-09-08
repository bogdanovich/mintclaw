package browser

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	localmcp "github.com/bogdanovich/mintclaw/pkg/mcp"
)

func ephemeralRuntimeFixture(t *testing.T) config.BrowserProfileRuntimeConfig {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	ephemeralRoot := filepath.Join(base, "ephemeral")
	lockRoot := filepath.Join(base, "locks")
	for _, path := range []string{ephemeralRoot, lockRoot} {
		if err = os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return config.BrowserProfileRuntimeConfig{
		EphemeralRoot: ephemeralRoot,
		LockFile:      filepath.Join(lockRoot, "ephemeral.lock"),
	}
}

func assertDirectoryEmpty(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory %q contains %#v", path, entries)
	}
}

func TestEphemeralRuntimeLeaseRemovesOnlyAnchoredSessionDirectory(t *testing.T) {
	runtime := ephemeralRuntimeFixture(t)
	outside := filepath.Join(filepath.Dir(runtime.EphemeralRoot), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideMarker := filepath.Join(outside, "keep")
	if err := os.WriteFile(outsideMarker, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	lease, err := createEphemeralRuntimeLease(runtime, "session_1")
	if err != nil {
		t.Fatal(err)
	}
	if info, statErr := os.Lstat(lease.Path()); statErr != nil || !info.IsDir() ||
		info.Mode().Perm() != 0o700 {
		t.Fatalf("session runtime = %#v, %v", info, statErr)
	}
	if err = os.WriteFile(filepath.Join(lease.Path(), "state"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(lease.Path(), "outside-link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err = lease.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err = os.Lstat(lease.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session runtime survived cleanup: %v", err)
	}
	if data, readErr := os.ReadFile(outsideMarker); readErr != nil || string(data) != "outside" {
		t.Fatalf("outside marker = %q, %v", data, readErr)
	}
	assertDirectoryEmpty(t, runtime.EphemeralRoot)
	if err = lease.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestEphemeralRuntimeLeaseFailsClosedOnRootSymlinkSwap(t *testing.T) {
	runtime := ephemeralRuntimeFixture(t)
	lease, err := createEphemeralRuntimeLease(runtime, "root_swap")
	if err != nil {
		t.Fatal(err)
	}
	movedRoot := runtime.EphemeralRoot + "-moved"
	outside := filepath.Join(filepath.Dir(runtime.EphemeralRoot), "outside")
	if err = os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(outside, "keep")
	if err = os.WriteFile(marker, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(runtime.EphemeralRoot, movedRoot); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, runtime.EphemeralRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err = lease.Close(); err == nil || !strings.Contains(err.Error(), "root identity changed") {
		t.Fatalf("Close() root swap error = %v", err)
	}
	if data, readErr := os.ReadFile(marker); readErr != nil || string(data) != "outside" {
		t.Fatalf("outside marker = %q, %v", data, readErr)
	}
	if err = os.Remove(runtime.EphemeralRoot); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(movedRoot, runtime.EphemeralRoot); err != nil {
		t.Fatal(err)
	}
	if err = lease.Close(); err != nil {
		t.Fatalf("Close() after restoring root error = %v", err)
	}
}

func TestEphemeralRuntimeLeaseFailsClosedOnSessionSwapAndPermissionChange(t *testing.T) {
	t.Run("symlink swap", func(t *testing.T) {
		runtime := ephemeralRuntimeFixture(t)
		lease, err := createEphemeralRuntimeLease(runtime, "session_swap")
		if err != nil {
			t.Fatal(err)
		}
		original := lease.Path()
		saved := original + "-saved"
		outside := filepath.Join(filepath.Dir(runtime.EphemeralRoot), "outside")
		if err = os.Mkdir(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(outside, "keep")
		if err = os.WriteFile(marker, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err = os.Rename(original, saved); err != nil {
			t.Fatal(err)
		}
		if err = os.Symlink(outside, original); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err = lease.Close(); err == nil || !strings.Contains(err.Error(), "session identity changed") {
			t.Fatalf("Close() session swap error = %v", err)
		}
		if data, readErr := os.ReadFile(marker); readErr != nil || string(data) != "outside" {
			t.Fatalf("outside marker = %q, %v", data, readErr)
		}
		if err = os.Remove(original); err != nil {
			t.Fatal(err)
		}
		if err = os.Rename(saved, original); err != nil {
			t.Fatal(err)
		}
		if err = lease.Close(); err != nil {
			t.Fatalf("Close() after restoring session error = %v", err)
		}
	})

	t.Run("permissive permissions", func(t *testing.T) {
		runtime := ephemeralRuntimeFixture(t)
		lease, err := createEphemeralRuntimeLease(runtime, "permission_change")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.Chmod(lease.Path(), 0o750); err != nil {
			t.Fatal(err)
		}
		if err = lease.Close(); err == nil || !strings.Contains(err.Error(), "session identity changed") {
			t.Fatalf("Close() permission error = %v", err)
		}
		if err = os.Chmod(lease.Path(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err = lease.Close(); err != nil {
			t.Fatalf("Close() after restoring permissions error = %v", err)
		}
	})
}

func TestEphemeralRuntimeLeaseQuarantinesCleanupFailureAndRetriesExactly(t *testing.T) {
	runtime := ephemeralRuntimeFixture(t)
	lease, err := createEphemeralRuntimeLease(runtime, "cleanup_retry")
	if err != nil {
		t.Fatal(err)
	}
	collision := filepath.Join(runtime.EphemeralRoot, lease.quarantineName)
	if err = os.Mkdir(collision, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = lease.Close(); err == nil || !strings.Contains(err.Error(), "quarantine identity") {
		t.Fatalf("Close() collision error = %v", err)
	}
	if _, err = os.Lstat(lease.Path()); err != nil {
		t.Fatalf("active runtime was not retained for exact cleanup retry: %v", err)
	}
	if err = os.Remove(collision); err != nil {
		t.Fatal(err)
	}
	if err = lease.Close(); err != nil {
		t.Fatalf("Close() retry error = %v", err)
	}
	assertDirectoryEmpty(t, runtime.EphemeralRoot)
}

func TestEphemeralRuntimeLeaseRetainsQuarantineAfterInjectedDeletionFailure(t *testing.T) {
	runtime := ephemeralRuntimeFixture(t)
	lease, err := createEphemeralRuntimeLease(runtime, "delete_failure")
	if err != nil {
		t.Fatal(err)
	}
	realRemoveAll := lease.removeAll
	failOnce := true
	lease.removeAll = func(name string) error {
		if failOnce {
			failOnce = false
			return errors.New("injected deletion failure")
		}
		return realRemoveAll(name)
	}
	if err = lease.Close(); err == nil || !strings.Contains(err.Error(), "remove browser ephemeral runtime") {
		t.Fatalf("Close() deletion error = %v", err)
	}
	if _, err = os.Lstat(lease.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active runtime remained after quarantine: %v", err)
	}
	quarantine := filepath.Join(runtime.EphemeralRoot, lease.quarantineName)
	if info, statErr := os.Lstat(quarantine); statErr != nil || !info.IsDir() {
		t.Fatalf("quarantined runtime = %#v, %v", info, statErr)
	}
	if _, createErr := createEphemeralRuntimeLease(runtime, "blocked"); createErr == nil ||
		!strings.Contains(createErr.Error(), "exclusive lease is busy") {
		t.Fatalf("create while quarantine cleanup is pending error = %v", createErr)
	}
	if err = lease.Close(); err != nil {
		t.Fatalf("Close() retry error = %v", err)
	}
	assertDirectoryEmpty(t, runtime.EphemeralRoot)
}

func TestRecoverEphemeralProfileRuntimeIsAllOrNothingAndLockBound(t *testing.T) {
	runtime := ephemeralRuntimeFixture(t)
	active := ephemeralRuntimePrefix + "0123456789abcdef"
	quarantined := ephemeralRuntimePrefix + "89abcdef01234567.quarantine"
	for _, name := range []string{active, quarantined} {
		if err := os.Mkdir(filepath.Join(runtime.EphemeralRoot, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	lease, err := localmcp.AcquireExclusiveServerLease("test", runtime.LockFile)
	if err != nil {
		t.Fatal(err)
	}
	if err = recoverEphemeralProfileRuntime(runtime); err == nil ||
		!strings.Contains(err.Error(), "exclusive lease is busy") {
		t.Fatalf("recovery while driver lock is held error = %v", err)
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err = recoverEphemeralProfileRuntime(runtime); err != nil {
		t.Fatalf("recoverEphemeralProfileRuntime() error = %v", err)
	}
	assertDirectoryEmpty(t, runtime.EphemeralRoot)
	activeLease, err := createEphemeralRuntimeLease(runtime, "active")
	if err != nil {
		t.Fatal(err)
	}
	if err = recoverEphemeralProfileRuntime(runtime); err == nil ||
		!strings.Contains(err.Error(), "exclusive lease is busy") {
		t.Fatalf("recovery while session lifecycle is held error = %v", err)
	}
	if second, createErr := createEphemeralRuntimeLease(runtime, "second"); createErr == nil || second != nil ||
		!strings.Contains(createErr.Error(), "exclusive lease is busy") {
		t.Fatalf("second runtime lease = %#v, %v", second, createErr)
	}
	if err = activeLease.Close(); err != nil {
		t.Fatal(err)
	}

	if err = os.Mkdir(filepath.Join(runtime.EphemeralRoot, active), 0o700); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(runtime.EphemeralRoot, "operator-file")
	if err = os.WriteFile(unknown, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = recoverEphemeralProfileRuntime(runtime); err == nil ||
		!strings.Contains(err.Error(), "unrecognized entry") {
		t.Fatalf("recovery with unknown entry error = %v", err)
	}
	if _, err = os.Lstat(filepath.Join(runtime.EphemeralRoot, active)); err != nil {
		t.Fatalf("recognized runtime was deleted before all entries were admitted: %v", err)
	}
}

func TestRecoverEphemeralProfileRuntimeRejectsUnsafeRecognizedEntry(t *testing.T) {
	for _, test := range []struct {
		name  string
		build func(string, string) error
	}{
		{
			name: "symlink",
			build: func(path, outside string) error {
				return os.Symlink(outside, path)
			},
		},
		{
			name: "permissions",
			build: func(path, _ string) error {
				return os.Mkdir(path, 0o750)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := ephemeralRuntimeFixture(t)
			outside := filepath.Join(filepath.Dir(runtime.EphemeralRoot), "outside")
			if err := os.Mkdir(outside, 0o700); err != nil {
				t.Fatal(err)
			}
			entry := filepath.Join(
				runtime.EphemeralRoot,
				ephemeralRuntimePrefix+"0123456789abcdef",
			)
			if err := test.build(entry, outside); err != nil {
				t.Skipf("unsafe fixture unavailable: %v", err)
			}
			if err := recoverEphemeralProfileRuntime(runtime); err == nil ||
				!strings.Contains(err.Error(), "is unsafe") {
				t.Fatalf("recovery unsafe entry error = %v", err)
			}
			if _, err := os.Lstat(entry); err != nil {
				t.Fatalf("unsafe entry was deleted: %v", err)
			}
		})
	}
}
