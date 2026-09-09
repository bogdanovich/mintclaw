//go:build linux || darwin

package thread

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func renameThreadReservationNoReplace(root *os.Root, oldName, newName string) error {
	if root == nil || !filepath.IsLocal(oldName) || !filepath.IsLocal(newName) {
		return fs.ErrInvalid
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	renameErr := renameThreadReservationAtNoReplace(int(directory.Fd()), oldName, newName)
	closeErr := directory.Close()
	if errors.Is(renameErr, syscall.EEXIST) {
		renameErr = fs.ErrExist
	}
	return errors.Join(renameErr, closeErr)
}
