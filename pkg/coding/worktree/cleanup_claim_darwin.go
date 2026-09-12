//go:build darwin

package worktree

import "golang.org/x/sys/unix"

func renameCleanupRootAtNoReplace(directory int, oldName, newName string) error {
	return unix.RenameatxNp(directory, oldName, directory, newName, unix.RENAME_EXCL)
}
