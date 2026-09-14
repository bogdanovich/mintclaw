//go:build windows

package document

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func acquireDocumentJournalFileLock(ctx context.Context, path string) (func(), error) {
	owner, err := currentWindowsDocumentJournalOwner()
	if err != nil {
		return nil, err
	}
	descriptor, _, err := ownerOnlyWindowsDocumentJournalDescriptor(owner, false)
	if err != nil {
		return nil, err
	}
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		attributes,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open document write journal lock: %w", err)
	}
	lock := os.NewFile(uintptr(handle), path)
	if lock == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("open document write journal lock descriptor")
	}
	if err = validateWindowsDocumentJournalType(handle, false); err != nil {
		_ = lock.Close()
		return nil, err
	}
	if err = validateOwnerOnlyWindowsDocumentJournalDACL(handle, owner, false); err != nil {
		_ = lock.Close()
		return nil, err
	}
	overlapped := &windows.Overlapped{}
	for {
		err = windows.LockFileEx(
			windows.Handle(lock.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			overlapped,
		)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			_ = lock.Close()
			return nil, fmt.Errorf("lock document write journal: %w", err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			_ = lock.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(lock.Fd()), 0, 1, 0, overlapped)
		_ = lock.Close()
	}, nil
}

func secureDocumentJournalRoot(path string) error {
	return secureWindowsDocumentJournalPath(path, true)
}

func secureDocumentJournalRecord(path string) error {
	return secureWindowsDocumentJournalPath(path, false)
}

func validateDocumentJournalRecordSecurity(path string, file *os.File, opened os.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !opened.Mode().IsRegular() ||
		!os.SameFile(opened, current) {
		return errors.New("document write journal record is unsafe")
	}
	handle := windows.Handle(file.Fd())
	if err = validateWindowsDocumentJournalType(handle, false); err != nil {
		return err
	}
	owner, err := currentWindowsDocumentJournalOwner()
	if err != nil {
		return err
	}
	return validateOwnerOnlyWindowsDocumentJournalDACL(handle, owner, false)
}

func currentWindowsDocumentJournalOwner() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("get current Windows document journal user: %w", err)
	}
	return user.User.Sid, nil
}

func ownerOnlyWindowsDocumentJournalDescriptor(
	owner *windows.SID,
	directory bool,
) (*windows.SECURITY_DESCRIPTOR, *windows.ACL, error) {
	aceFlags := ""
	if directory {
		aceFlags = "OICI"
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		"O:" + owner.String() + "D:P(A;" + aceFlags + ";GA;;;" + owner.String() + ")",
	)
	if err != nil {
		return nil, nil, fmt.Errorf("build owner-only Windows document journal descriptor: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return nil, nil, fmt.Errorf("read owner-only Windows document journal DACL: %w", err)
	}
	return descriptor, dacl, nil
}

func secureWindowsDocumentJournalPath(path string, directory bool) error {
	owner, err := currentWindowsDocumentJournalOwner()
	if err != nil {
		return err
	}
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	flags := uint32(windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags = windows.FILE_FLAG_BACKUP_SEMANTICS | windows.FILE_FLAG_OPEN_REPARSE_POINT
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		flags,
		0,
	)
	if err != nil {
		return fmt.Errorf("open Windows document journal path: %w", err)
	}
	defer windows.CloseHandle(handle)
	if err = validateWindowsDocumentJournalType(handle, directory); err != nil {
		return err
	}
	_, dacl, err := ownerOnlyWindowsDocumentJournalDescriptor(owner, directory)
	if err != nil {
		return err
	}
	information := windows.SECURITY_INFORMATION(
		windows.OWNER_SECURITY_INFORMATION |
			windows.DACL_SECURITY_INFORMATION |
			windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)
	if err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, information, owner, nil, dacl, nil); err != nil {
		return fmt.Errorf("apply owner-only Windows document journal DACL: %w", err)
	}
	return validateOwnerOnlyWindowsDocumentJournalDACL(handle, owner, directory)
}

func validateWindowsDocumentJournalType(handle windows.Handle, directory bool) error {
	fileType, err := windows.GetFileType(handle)
	if err != nil || fileType != windows.FILE_TYPE_DISK {
		return errors.New("Windows document journal path type is unsafe")
	}
	var information windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &information); err != nil {
		return fmt.Errorf("inspect Windows document journal path: %w", err)
	}
	isDirectory := information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || isDirectory != directory ||
		(!directory && information.NumberOfLinks != 1) {
		return errors.New("Windows document journal path is unsafe")
	}
	return nil
}

func validateOwnerOnlyWindowsDocumentJournalDACL(
	handle windows.Handle,
	owner *windows.SID,
	directory bool,
) error {
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read Windows document journal security descriptor: %w", err)
	}
	actualOwner, _, err := descriptor.Owner()
	if err != nil || !actualOwner.Equals(owner) {
		return errors.New("Windows document journal owner validation failed")
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("Windows document journal DACL is not protected")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		return errors.New("Windows document journal DACL is not owner-only")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err = windows.GetAce(dacl, 0, &ace); err != nil {
		return fmt.Errorf("read Windows document journal DACL entry: %w", err)
	}
	aceOwner := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	const fileAllAccess windows.ACCESS_MASK = 0x1F01FF
	fullControl := ace.Mask&windows.GENERIC_ALL != 0 || ace.Mask&fileAllAccess == fileAllAccess
	wantInheritance := uint8(0)
	if directory {
		wantInheritance = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	actualInheritance := ace.Header.AceFlags & (windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !fullControl || !aceOwner.Equals(owner) ||
		actualInheritance != wantInheritance {
		return errors.New("Windows document journal DACL is not owner-only")
	}
	return nil
}
