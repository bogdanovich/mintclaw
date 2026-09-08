//go:build windows

package document

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func openOutputIdentity(path string) (*os.File, error) {
	return os.Open(path)
}

func renamePathNoReplace(oldPath, newPath string) error {
	oldPointer, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return err
	}
	newPointer, err := windows.UTF16PtrFromString(newPath)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(oldPointer, newPointer, windows.MOVEFILE_WRITE_THROUGH)
}

func exchangePaths(_, _ string) error {
	return errors.New("atomic document output replacement is unavailable on this platform")
}
