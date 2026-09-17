//go:build linux || darwin

package worktree

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func renameCleanupRootNoReplace(root *os.Root, oldName, newName string) error {
	if root == nil || !filepath.IsLocal(oldName) || !filepath.IsLocal(newName) {
		return fs.ErrInvalid
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	renameErr := renameCleanupRootAtNoReplace(int(directory.Fd()), oldName, newName)
	closeErr := directory.Close()
	if errors.Is(renameErr, syscall.EEXIST) {
		renameErr = fs.ErrExist
	}
	return errors.Join(renameErr, closeErr)
}
