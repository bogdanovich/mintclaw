//go:build windows

package document

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWriteJournalRejectsExistingWindowsLockWithBroadDACL(t *testing.T) {
	journal, err := NewWriteJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	pathPtr, err := windows.UTF16PtrFromString(journal.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;WD)")
	if err != nil {
		_ = windows.CloseHandle(handle)
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		_ = windows.CloseHandle(handle)
		t.Fatal(err)
	}
	information := windows.SECURITY_INFORMATION(
		windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)
	if err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, information, nil, nil, dacl, nil); err != nil {
		_ = windows.CloseHandle(handle)
		t.Fatal(err)
	}
	if err = windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	_, _, err = journal.Accept(
		t.Context(),
		"document_write_unsafe_windows_lock",
		writeTestOwner(),
		normalizedWriteTestRequest(t, "safe value"),
	)
	if !errors.Is(err, ErrWriteJournalFailed) {
		t.Fatalf("unsafe Windows lock error = %v", err)
	}
}

func assertPrivateDocumentJournalPath(t *testing.T, path string, directory bool) {
	t.Helper()
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	flags := uint32(windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags = windows.FILE_FLAG_BACKUP_SEMANTICS | windows.FILE_FLAG_OPEN_REPARSE_POINT
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		flags,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(handle), path)
	defer func() { _ = file.Close() }()
	if err = validateWindowsDocumentJournalType(handle, directory); err != nil {
		t.Fatal(err)
	}
	owner, err := currentWindowsDocumentJournalOwner()
	if err != nil {
		t.Fatal(err)
	}
	if err = validateOwnerOnlyWindowsDocumentJournalDACL(handle, owner, directory); err != nil {
		t.Fatal(err)
	}
}
