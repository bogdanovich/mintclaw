//go:build linux

package document

import (
	"os"

	"golang.org/x/sys/unix"
)

func openOutputIdentity(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

func renamePathNoReplace(oldPath, newPath string) error {
	return unix.Renameat2(
		unix.AT_FDCWD,
		oldPath,
		unix.AT_FDCWD,
		newPath,
		unix.RENAME_NOREPLACE,
	)
}
