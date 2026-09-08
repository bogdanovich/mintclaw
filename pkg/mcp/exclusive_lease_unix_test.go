//go:build !windows

package mcp

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const exclusiveLeaseHelperPathEnv = "MINTCLAW_EXCLUSIVE_LEASE_HELPER_PATH"

func TestExclusiveServerLeaseCrossProcessHelper(t *testing.T) {
	path := os.Getenv(exclusiveLeaseHelperPathEnv)
	if path == "" {
		return
	}
	lease, err := acquireExclusiveServerLease("helper", path)
	if lease != nil {
		lease.release()
		t.Fatal("helper acquired the rebound canonical lease")
	}
	if !errors.Is(err, errExclusiveLeaseBusy) {
		t.Fatalf("helper acquire error = %v, want busy", err)
	}
}

func TestExclusiveServerLeaseRejectsCrossProcessParentRebindContender(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "locks")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "playwright.lock")
	lease, err := acquireExclusiveServerLease("playwright", path)
	if err != nil {
		t.Fatal(err)
	}
	moved := parent + "-moved"
	if err = os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestExclusiveServerLeaseCrossProcessHelper$")
	command.Env = append(os.Environ(), exclusiveLeaseHelperPathEnv+"="+path)
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Fatalf("helper-process contender error = %v, output = %s", runErr, output)
	}
	if err = os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(moved, parent); err != nil {
		t.Fatal(err)
	}
	if err = lease.close(); err != nil {
		t.Fatalf("release after parent restore error = %v", err)
	}
}

func assertExclusiveLeaseFileSecurity(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("lock file mode = %o, want 600", mode)
	}
}

func TestAcquireExclusiveServerLeaseRejectsSymlinkWithoutMutatingTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("unchanged"), 0o700); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chmod(target, 0o700); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	leasePath := filepath.Join(root, "playwright.lock")
	if err := os.Symlink(target, leasePath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	lease, err := AcquireExclusiveServerLease("playwright", leasePath)
	if err == nil {
		_ = lease.Close()
		t.Fatal("AcquireExclusiveServerLease() accepted a symbolic link")
	}
	info, statErr := os.Stat(target)
	if statErr != nil {
		t.Fatalf("Stat() error = %v", statErr)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("target permissions = %04o, want 0700", got)
	}
	contents, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if got := string(contents); got != "unchanged" {
		t.Fatalf("target contents = %q, want unchanged", got)
	}
}

func TestAcquireExclusiveServerLeaseRejectsHardLinkWithoutMutatingTarget(t *testing.T) {
	for _, mode := range []os.FileMode{0o700, 0o600} {
		t.Run(mode.String(), func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "outside")
			if err := os.WriteFile(target, []byte("unchanged"), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(target, mode); err != nil {
				t.Fatal(err)
			}
			leasePath := filepath.Join(root, "playwright.lock")
			if err := os.Link(target, leasePath); err != nil {
				t.Skipf("hard links unavailable: %v", err)
			}

			lease, err := AcquireExclusiveServerLease("playwright", leasePath)
			if err == nil {
				_ = lease.Close()
				t.Fatal("AcquireExclusiveServerLease() accepted a hard link")
			}
			info, statErr := os.Stat(target)
			if statErr != nil {
				t.Fatal(statErr)
			}
			if got := info.Mode().Perm(); got != mode {
				t.Fatalf("hard-link target permissions = %04o, want %04o", got, mode)
			}
			contents, readErr := os.ReadFile(target)
			if readErr != nil || string(contents) != "unchanged" {
				t.Fatalf("hard-link target contents = %q, %v", contents, readErr)
			}
		})
	}
}

func TestAcquireExclusiveServerLeaseRejectsPermissiveExistingFileWithoutRepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playwright.lock")
	if err := os.WriteFile(path, []byte("existing"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	lease, err := AcquireExclusiveServerLease("playwright", path)
	if err == nil {
		_ = lease.Close()
		t.Fatal("AcquireExclusiveServerLease() repaired an unsafe existing file")
	}
	info, statErr := os.Stat(path)
	if statErr != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("existing file mode = %#v, %v; want unchanged 0700", info, statErr)
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil || string(contents) != "existing" {
		t.Fatalf("existing file contents = %q, %v", contents, readErr)
	}
}

func TestAcquireExclusiveServerLeaseRejectsSymlinkedParentWithoutMutatingTarget(t *testing.T) {
	root := t.TempDir()
	lockParent := filepath.Join(root, "locks")
	if err := os.Mkdir(lockParent, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	outside := t.TempDir()
	target := filepath.Join(outside, "playwright.lock")
	if err := os.WriteFile(target, []byte("unchanged"), 0o700); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chmod(target, 0o700); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	if err := os.Rename(lockParent, lockParent+"-validated"); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	if err := os.Symlink(outside, lockParent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	lease, err := AcquireExclusiveServerLease(
		"playwright",
		filepath.Join(lockParent, "playwright.lock"),
	)
	if err == nil {
		_ = lease.Close()
		t.Fatal("AcquireExclusiveServerLease() accepted a symlinked parent")
	}
	info, statErr := os.Stat(target)
	if statErr != nil {
		t.Fatalf("Stat() error = %v", statErr)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("target permissions = %04o, want 0700", got)
	}
	contents, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if got := string(contents); got != "unchanged" {
		t.Fatalf("target contents = %q, want unchanged", got)
	}
}
