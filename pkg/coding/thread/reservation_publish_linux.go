//go:build linux

package thread

import "golang.org/x/sys/unix"

func renameThreadReservationAtNoReplace(directory int, oldName, newName string) error {
	return unix.Renameat2(directory, oldName, directory, newName, unix.RENAME_NOREPLACE)
}
