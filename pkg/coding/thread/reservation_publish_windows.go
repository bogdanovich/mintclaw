//go:build windows

package thread

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

type threadReservationRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func renameThreadReservationNoReplace(root *os.Root, oldName, newName string) error {
	if root == nil || !filepath.IsLocal(oldName) || !filepath.IsLocal(newName) {
		return fs.ErrInvalid
	}
	oldName = filepath.Clean(oldName)
	newName = filepath.Clean(newName)
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open no-replace rename root: %w", err)
	}
	parentHandle := windows.Handle(directory.Fd())
	sourceHandle, err := openWindowsCatalogChild(
		parentHandle,
		oldName,
		windows.DELETE|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT,
	)
	if err != nil {
		return errors.Join(fmt.Errorf("open no-replace rename source: %w", err), directory.Close())
	}
	info, infoErr := windowsCatalogHandleInfo(sourceHandle)
	if infoErr != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		if infoErr == nil {
			infoErr = fs.ErrInvalid
		}
		return errors.Join(infoErr, windows.CloseHandle(sourceHandle), directory.Close())
	}
	// NT relative rename information requires the destination-directory
	// handle and a simple leaf name, not a compound path relative to root.
	destinationHandle := parentHandle
	if destinationParent := filepath.Dir(newName); destinationParent != "." {
		destination, destinationErr := openCatalogChildDirectory(&catalogDirectory{file: directory}, destinationParent)
		if destinationErr != nil {
			return errors.Join(
				fmt.Errorf("open no-replace rename destination: %w", destinationErr),
				windows.CloseHandle(sourceHandle),
				directory.Close(),
			)
		}
		defer func() { _ = destination.Close() }()
		destinationHandle = windows.Handle(destination.file.Fd())
	}
	name, err := windows.UTF16FromString(filepath.Base(newName))
	if err != nil {
		return errors.Join(err, windows.CloseHandle(sourceHandle), directory.Close())
	}
	nameBytes := (len(name) - 1) * 2
	var template threadReservationRenameInformation
	buffer := make([]byte, int(unsafe.Offsetof(template.FileName))+nameBytes)
	rename := (*threadReservationRenameInformation)(unsafe.Pointer(&buffer[0]))
	rename.RootDirectory = destinationHandle
	rename.FileNameLength = uint32(nameBytes)
	copy(
		(*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&rename.FileName[0]))[:nameBytes/2:nameBytes/2],
		name,
	)
	var status windows.IO_STATUS_BLOCK
	renameErr := windows.NtSetInformationFile(
		sourceHandle,
		&status,
		&buffer[0],
		uint32(len(buffer)),
		windows.FileRenameInformation,
	)
	if errors.Is(renameErr, windows.STATUS_OBJECT_NAME_COLLISION) ||
		errors.Is(renameErr, windows.STATUS_OBJECT_NAME_EXISTS) {
		renameErr = fs.ErrExist
	}
	if renameErr != nil {
		renameErr = fmt.Errorf("publish no-replace directory rename: %w", renameErr)
	}
	return errors.Join(renameErr, windows.CloseHandle(sourceHandle), directory.Close())
}
