//go:build windows

package mcp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func exclusiveLeaseReservationKey(path string) string { return strings.ToLower(path) }

func openExclusiveLeaseFile(path string) (*os.File, *exclusiveLeaseParent, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, nil, fmt.Errorf("get current Windows user: %w", err)
	}
	owner := user.User.Sid
	descriptor, err := windows.SecurityDescriptorFromString(
		"O:" + owner.String() + "D:P(A;;GA;;;" + owner.String() + ")",
	)
	if err != nil {
		return nil, nil, fmt.Errorf("build owner-only Windows security descriptor: %w", err)
	}

	parent, leaf, err := openWindowsLeaseParent(path)
	if err != nil {
		return nil, nil, err
	}
	handle, err := openWindowsLeaseRelative(
		windows.Handle(parent.file.Fd()),
		leaf,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.WRITE_DAC,
		descriptor,
		windows.FILE_OPEN_IF,
		windows.FILE_NON_DIRECTORY_FILE,
	)
	if err != nil {
		parent.close()
		return nil, nil, err
	}
	closeOnError := func(err error) (*os.File, *exclusiveLeaseParent, error) {
		_ = windows.CloseHandle(handle)
		parent.close()
		return nil, nil, err
	}

	if err := validateWindowsLeaseFileType(handle); err != nil {
		return closeOnError(err)
	}
	if err := validateWindowsLeaseOwner(handle, owner); err != nil {
		return closeOnError(err)
	}
	if err := validateOwnerOnlyWindowsDACL(handle, owner); err != nil {
		return closeOnError(err)
	}
	file := os.NewFile(uintptr(handle), path)
	parent.leaf = leaf
	if err := parent.validateLeaf(file); err != nil {
		_ = file.Close()
		parent.close()
		return nil, nil, err
	}
	return file, parent, nil
}

func openWindowsLeaseParent(path string) (*exclusiveLeaseParent, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, "", errExclusiveLeaseUnsafe
	}
	leaf := filepath.Base(path)
	if filepath.VolumeName(path) == "" || leaf == "." || leaf == string(filepath.Separator) {
		return nil, "", errExclusiveLeaseUnsafe
	}
	parentPath := filepath.Dir(path)
	configuredInfo, err := os.Lstat(parentPath)
	if err != nil || !configuredInfo.IsDir() || configuredInfo.Mode()&os.ModeSymlink != 0 {
		return nil, "", errExclusiveLeaseUnsafe
	}
	resolvedParent, err := filepath.EvalSymlinks(parentPath)
	if err != nil {
		return nil, "", err
	}
	resolvedInfo, err := os.Lstat(resolvedParent)
	if err != nil || !os.SameFile(configuredInfo, resolvedInfo) {
		return nil, "", errExclusiveLeaseUnsafe
	}
	parentPtr, err := windows.UTF16PtrFromString(resolvedParent)
	if err != nil {
		return nil, "", err
	}
	handle, err := windows.CreateFile(
		parentPtr,
		windows.FILE_TRAVERSE|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, "", err
	}
	if err = validateWindowsLeaseDirectory(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, "", err
	}
	parent := os.NewFile(uintptr(handle), resolvedParent)
	anchored := &exclusiveLeaseParent{
		file: parent, path: parentPath, identity: configuredInfo,
	}
	if err = anchored.validate(); err != nil {
		anchored.close()
		return nil, "", errExclusiveLeaseUnsafe
	}
	return anchored, leaf, nil
}

func openWindowsLeaseRelative(
	parent windows.Handle,
	name string,
	access uint32,
	descriptor *windows.SECURITY_DESCRIPTOR,
	disposition uint32,
	typeOption uint32,
) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:             uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory:      parent,
		ObjectName:         objectName,
		Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		SecurityDescriptor: descriptor,
	}
	var (
		handle         windows.Handle
		ioStatus       windows.IO_STATUS_BLOCK
		allocationSize int64
	)
	err = windows.NtCreateFile(
		&handle,
		access,
		attributes,
		&ioStatus,
		&allocationSize,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		disposition,
		typeOption|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return handle, nil
}

func validateWindowsLeaseDirectory(handle windows.Handle) error {
	fileType, err := windows.GetFileType(handle)
	if err != nil || fileType != windows.FILE_TYPE_DISK {
		return errExclusiveLeaseUnsafe
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fmt.Errorf("read Windows lease directory information: %w", err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errExclusiveLeaseUnsafe
	}
	return nil
}

func validateWindowsLeaseFileType(handle windows.Handle) error {
	fileType, err := windows.GetFileType(handle)
	if err != nil || fileType != windows.FILE_TYPE_DISK {
		return errExclusiveLeaseUnsafe
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fmt.Errorf("read Windows lease file information: %w", err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 ||
		info.NumberOfLinks != 1 {
		return errExclusiveLeaseUnsafe
	}
	return nil
}

func validateWindowsLeaseOwner(handle windows.Handle, owner *windows.SID) error {
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read Windows lease owner: %w", err)
	}
	actualOwner, _, err := descriptor.Owner()
	if err != nil || !actualOwner.Equals(owner) {
		return fmt.Errorf("Windows lease owner validation failed")
	}
	return nil
}

func validateOwnerOnlyWindowsDACL(handle windows.Handle, owner *windows.SID) error {
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read Windows lease security descriptor: %w", err)
	}
	actualOwner, _, err := descriptor.Owner()
	if err != nil || !actualOwner.Equals(owner) {
		return fmt.Errorf("Windows lease owner validation failed")
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("Windows lease DACL is not protected")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		return fmt.Errorf("Windows lease DACL is not owner-only")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return fmt.Errorf("read Windows lease DACL entry: %w", err)
	}
	aceOwner := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	const fileAllAccess windows.ACCESS_MASK = 0x1F01FF
	grantsFullControl := ace.Mask&windows.GENERIC_ALL != 0 || ace.Mask&fileAllAccess == fileAllAccess
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
		!grantsFullControl ||
		!aceOwner.Equals(owner) {
		return fmt.Errorf("Windows lease DACL is not owner-only")
	}
	return nil
}

func tryAcquireExclusiveFileLock(file *os.File) error {
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&windows.Overlapped{},
	)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errExclusiveLeaseBusy
	}
	return err
}

func releaseExclusiveFileLock(file *os.File) error {
	return windows.UnlockFileEx(
		windows.Handle(file.Fd()),
		0,
		1,
		0,
		&windows.Overlapped{},
	)
}
