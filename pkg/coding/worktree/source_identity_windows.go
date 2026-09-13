//go:build windows

package worktree

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func inspectDirectoryIdentity(path string) (FilesystemIdentity, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return FilesystemIdentity{}, err
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return FilesystemIdentity{}, err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return FilesystemIdentity{}, err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return FilesystemIdentity{}, fmt.Errorf("source directory was replaced or redirected")
	}
	return FilesystemIdentity{
		Volume: uint64(information.VolumeSerialNumber),
		File:   uint64(information.FileIndexHigh)<<32 | uint64(information.FileIndexLow),
	}, nil
}
