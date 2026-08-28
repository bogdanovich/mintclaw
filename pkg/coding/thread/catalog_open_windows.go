//go:build windows

package thread

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

type catalogDirectory struct {
	file *os.File
}

func openCatalogRoot(path string) (*catalogDirectory, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	return newWindowsCatalogDirectory(handle, path)
}

func openCatalogChildDirectory(parent *catalogDirectory, name string) (*catalogDirectory, error) {
	if parent == nil || parent.file == nil || !filepath.IsLocal(name) {
		return nil, fmt.Errorf("catalog directory and local child name are required")
	}
	handle, err := openWindowsCatalogChild(
		windows.Handle(parent.file.Fd()),
		name,
		windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT,
	)
	if err != nil {
		return nil, err
	}
	return newWindowsCatalogDirectory(handle, filepath.Join(parent.file.Name(), name))
}

func newWindowsCatalogDirectory(handle windows.Handle, name string) (*catalogDirectory, error) {
	info, err := windowsCatalogHandleInfo(handle)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("catalog directory handle is a reparse point or non-directory")
	}
	file := os.NewFile(uintptr(handle), name)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("create catalog directory handle")
	}
	return &catalogDirectory{file: file}, nil
}

func (d *catalogDirectory) readDir(count int) ([]os.DirEntry, error) {
	return d.file.ReadDir(count)
}

func (d *catalogDirectory) Close() error {
	if d == nil || d.file == nil {
		return nil
	}
	return d.file.Close()
}

func openCatalogMetadataFile(root *catalogDirectory) (*os.File, error) {
	return openCatalogFile(root, metadataFileName)
}

func openCatalogFile(root *catalogDirectory, name string) (*os.File, error) {
	if root == nil || root.file == nil || !filepath.IsLocal(name) {
		return nil, fmt.Errorf("catalog directory and local file name are required")
	}
	handle, err := openWindowsCatalogChild(
		windows.Handle(root.file.Fd()),
		name,
		windows.FILE_GENERIC_READ,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT,
	)
	if err != nil {
		return nil, err
	}
	info, err := windowsCatalogHandleInfo(handle)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("catalog file handle is a reparse point or directory")
	}
	file := os.NewFile(uintptr(handle), filepath.Join(root.file.Name(), name))
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("create catalog file handle")
	}
	return file, nil
}

func openWindowsCatalogChild(
	parent windows.Handle,
	name string,
	access uint32,
	options uint32,
) (windows.Handle, error) {
	return openWindowsCatalogChildWithDisposition(
		parent,
		name,
		access,
		windows.FILE_OPEN,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		options,
	)
}

func openWindowsCatalogChildWithDisposition(
	parent windows.Handle,
	name string,
	access uint32,
	disposition uint32,
	shareMode uint32,
	options uint32,
) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: parent,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var handle windows.Handle
	err = windows.NtCreateFile(
		&handle,
		access,
		attributes,
		&windows.IO_STATUS_BLOCK{},
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		shareMode,
		disposition,
		options,
		0,
		0,
	)
	if err != nil {
		if err == windows.STATUS_OBJECT_NAME_NOT_FOUND || err == windows.STATUS_OBJECT_PATH_NOT_FOUND {
			return windows.InvalidHandle, os.ErrNotExist
		}
		return windows.InvalidHandle, err
	}
	return handle, nil
}

func windowsCatalogHandleInfo(handle windows.Handle) (windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return windows.ByHandleFileInformation{}, err
	}
	return info, nil
}
