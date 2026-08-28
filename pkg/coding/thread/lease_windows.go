//go:build windows

package thread

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openThreadLeaseFile(root *catalogDirectory) (*os.File, error) {
	if root == nil || root.file == nil {
		return nil, fmt.Errorf("coding thread lease: thread directory is closed")
	}
	handle, err := openWindowsCatalogChildWithDisposition(
		windows.Handle(root.file.Fd()),
		leaseFileName,
		windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.READ_CONTROL|
			windows.WRITE_DAC|windows.WRITE_OWNER,
		windows.FILE_OPEN_IF,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT,
	)
	if err != nil {
		return nil, err
	}
	closeOnError := func(err error) (*os.File, error) {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	info, err := windowsCatalogHandleInfo(handle)
	if err != nil {
		return closeOnError(err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 ||
		info.NumberOfLinks != 1 {
		return closeOnError(fmt.Errorf(
			"coding thread lease: lock file is a reparse point, directory, or has multiple hard links",
		))
	}
	if err := secureWindowsThreadLease(handle); err != nil {
		return closeOnError(err)
	}
	file := os.NewFile(uintptr(handle), filepath.Join(root.file.Name(), leaseFileName))
	if file == nil {
		return closeOnError(fmt.Errorf("coding thread lease: create file handle"))
	}
	return file, nil
}

func secureWindowsThreadLease(handle windows.Handle) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("coding thread lease: get current Windows user: %w", err)
	}
	owner := user.User.Sid
	descriptor, err := windows.SecurityDescriptorFromString(
		"O:" + owner.String() + "D:P(A;;GA;;;" + owner.String() + ")",
	)
	if err != nil {
		return fmt.Errorf("coding thread lease: build owner-only Windows descriptor: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("coding thread lease: read owner-only Windows DACL: %w", err)
	}
	securityInformation := windows.SECURITY_INFORMATION(
		windows.OWNER_SECURITY_INFORMATION |
			windows.DACL_SECURITY_INFORMATION |
			windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)
	if err := windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		securityInformation,
		owner,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("coding thread lease: apply owner-only Windows security descriptor: %w", err)
	}
	return validateWindowsThreadLeaseSecurity(handle, owner)
}

func validateWindowsThreadLeaseSecurity(handle windows.Handle, owner *windows.SID) error {
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("coding thread lease: read Windows security descriptor: %w", err)
	}
	actualOwner, _, err := descriptor.Owner()
	if err != nil || !actualOwner.Equals(owner) {
		return fmt.Errorf("coding thread lease: Windows owner validation failed")
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("coding thread lease: Windows DACL is not protected")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		return fmt.Errorf("coding thread lease: Windows DACL is not owner-only")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return fmt.Errorf("coding thread lease: read Windows DACL entry: %w", err)
	}
	aceOwner := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	const fileAllAccess windows.ACCESS_MASK = 0x1F01FF
	grantsFullControl := ace.Mask&windows.GENERIC_ALL != 0 || ace.Mask&fileAllAccess == fileAllAccess
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !grantsFullControl || !aceOwner.Equals(owner) {
		return fmt.Errorf("coding thread lease: Windows DACL is not owner-only")
	}
	return nil
}

func tryAcquireThreadLeaseFile(file *os.File) error {
	overlapped := threadLeaseOverlapped()
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrLeaseBusy
	}
	return err
}

func releaseThreadLeaseFile(file *os.File) error {
	overlapped := threadLeaseOverlapped()
	return windows.UnlockFileEx(
		windows.Handle(file.Fd()),
		0,
		1,
		0,
		&overlapped,
	)
}

func threadLeaseOverlapped() windows.Overlapped {
	return windows.Overlapped{Offset: uint32(MaxLeaseOwnerBytes)}
}
