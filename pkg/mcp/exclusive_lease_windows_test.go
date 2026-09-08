//go:build windows

package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func assertExclusiveLeaseFileSecurity(t *testing.T, path string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	defer func() { _ = file.Close() }()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("GetTokenUser() error = %v", err)
	}
	if err := validateOwnerOnlyWindowsDACL(windows.Handle(file.Fd()), user.User.Sid); err != nil {
		t.Fatalf("validateOwnerOnlyWindowsDACL() error = %v", err)
	}
}

func TestOpenExclusiveLeaseFileReplacesInheritedWindowsDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playwright.lock")
	if err := os.WriteFile(path, []byte("existing"), 0o666); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	file, err := openExclusiveLeaseFile(path)
	if err != nil {
		t.Fatalf("openExclusiveLeaseFile() error = %v", err)
	}
	defer func() { _ = file.Close() }()

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("GetTokenUser() error = %v", err)
	}
	if err := validateOwnerOnlyWindowsDACL(windows.Handle(file.Fd()), user.User.Sid); err != nil {
		t.Fatalf("validateOwnerOnlyWindowsDACL() error = %v", err)
	}
}

func TestOpenExclusiveLeaseFileRejectsWindowsReparsePoint(t *testing.T) {
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	leasePath := filepath.Join(t.TempDir(), "playwright.lock")
	if err := os.Symlink(target, leasePath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	file, err := openExclusiveLeaseFile(leasePath)
	if err == nil {
		_ = file.Close()
		t.Fatal("openExclusiveLeaseFile() accepted a reparse point")
	}
	contents, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if got := string(contents); got != "unchanged" {
		t.Fatalf("target contents = %q, want unchanged", got)
	}
}

func TestOpenExclusiveLeaseFileRejectsWindowsReparseParentWithoutMutatingTarget(t *testing.T) {
	root := t.TempDir()
	lockParent := filepath.Join(root, "locks")
	if err := os.Mkdir(lockParent, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	outside := t.TempDir()
	target := filepath.Join(outside, "playwright.lock")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	securityInformation := windows.SECURITY_INFORMATION(
		windows.OWNER_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION,
	)
	before, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, securityInformation)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo(before) error = %v", err)
	}
	if err = os.Rename(lockParent, lockParent+"-validated"); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	if err = os.Symlink(outside, lockParent); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}

	file, err := openExclusiveLeaseFile(filepath.Join(lockParent, "playwright.lock"))
	if err == nil {
		_ = file.Close()
		t.Fatal("openExclusiveLeaseFile() accepted a reparse parent")
	}
	after, securityErr := windows.GetNamedSecurityInfo(
		target,
		windows.SE_FILE_OBJECT,
		securityInformation,
	)
	if securityErr != nil {
		t.Fatalf("GetNamedSecurityInfo(after) error = %v", securityErr)
	}
	if before.String() != after.String() {
		t.Fatalf("target security descriptor changed: before %q, after %q", before, after)
	}
	contents, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if got := string(contents); got != "unchanged" {
		t.Fatalf("target contents = %q, want unchanged", got)
	}
}
