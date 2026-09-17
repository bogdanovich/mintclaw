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

func TestOpenExclusiveLeaseFileCreatesOwnerOnlyWindowsDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playwright.lock")

	file, parent, err := openExclusiveLeaseFile(path)
	if err != nil {
		t.Fatalf("openExclusiveLeaseFile() error = %v", err)
	}
	defer func() { _ = file.Close() }()
	defer parent.close()

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("GetTokenUser() error = %v", err)
	}
	if err := validateOwnerOnlyWindowsDACL(windows.Handle(file.Fd()), user.User.Sid); err != nil {
		t.Fatalf("validateOwnerOnlyWindowsDACL() error = %v", err)
	}
}

func TestOpenExclusiveLeaseFileRejectsExistingWindowsDACLWithoutRepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playwright.lock")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		"O:" + user.User.Sid.String() +
			"D:P(A;;GA;;;" + user.User.Sid.String() + ")(A;;GR;;;WD)",
	)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		windows.StringToUTF16Ptr(path),
		windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	securityInformation := windows.SECURITY_INFORMATION(
		windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)
	if err = windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		securityInformation,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		_ = windows.CloseHandle(handle)
		t.Fatal(err)
	}
	if err = windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	readSecurity := windows.SECURITY_INFORMATION(
		windows.OWNER_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION,
	)
	before, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, readSecurity)
	if err != nil {
		t.Fatal(err)
	}

	file, parent, err := openExclusiveLeaseFile(path)
	if err == nil {
		_ = file.Close()
		parent.close()
		t.Fatal("openExclusiveLeaseFile() repaired an unsafe existing DACL")
	}
	after, securityErr := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, readSecurity)
	if securityErr != nil {
		t.Fatal(securityErr)
	}
	if before.String() != after.String() {
		t.Fatalf("existing security descriptor changed: before %q, after %q", before, after)
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

	file, parent, err := openExclusiveLeaseFile(leasePath)
	if err == nil {
		_ = file.Close()
		parent.close()
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

	file, parent, err := openExclusiveLeaseFile(filepath.Join(lockParent, "playwright.lock"))
	if err == nil {
		_ = file.Close()
		parent.close()
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

func TestOpenExclusiveLeaseFileRejectsWindowsHardLinkWithoutMutatingTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "outside")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(root, "playwright.lock")
	if err := os.Link(target, leasePath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	securityInformation := windows.SECURITY_INFORMATION(
		windows.OWNER_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION,
	)
	before, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, securityInformation)
	if err != nil {
		t.Fatal(err)
	}

	file, parent, err := openExclusiveLeaseFile(leasePath)
	if err == nil {
		_ = file.Close()
		parent.close()
		t.Fatal("openExclusiveLeaseFile() accepted a hard link")
	}
	after, securityErr := windows.GetNamedSecurityInfo(
		target,
		windows.SE_FILE_OBJECT,
		securityInformation,
	)
	if securityErr != nil {
		t.Fatal(securityErr)
	}
	if before.String() != after.String() {
		t.Fatalf("hard-link target security descriptor changed: before %q, after %q", before, after)
	}
	contents, readErr := os.ReadFile(target)
	if readErr != nil || string(contents) != "unchanged" {
		t.Fatalf("hard-link target contents = %q, %v", contents, readErr)
	}
}
