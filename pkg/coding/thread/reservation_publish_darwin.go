//go:build darwin

package thread

import "golang.org/x/sys/unix"

func renameThreadReservationAtNoReplace(directory int, oldName, newName string) error {
	return unix.RenameatxNp(directory, oldName, directory, newName, unix.RENAME_EXCL)
}
