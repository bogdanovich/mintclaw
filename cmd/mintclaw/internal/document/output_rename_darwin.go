//go:build darwin

package document

import (
	"os"

	"golang.org/x/sys/unix"
)

func openOutputIdentity(path string) (*os.File, error) {
	return os.Open(path)
}

func renamePathNoReplace(oldPath, newPath string) error {
	return unix.RenameatxNp(
		unix.AT_FDCWD,
		oldPath,
		unix.AT_FDCWD,
		newPath,
		unix.RENAME_EXCL,
	)
}

func exchangePaths(left, right string) error {
	return unix.RenameatxNp(
		unix.AT_FDCWD,
		left,
		unix.AT_FDCWD,
		right,
		unix.RENAME_SWAP,
	)
}
