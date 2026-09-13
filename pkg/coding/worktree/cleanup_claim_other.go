//go:build !linux && !darwin

package worktree

import (
	"errors"
	"os"
)

func renameCleanupRootNoReplace(*os.Root, string, string) error {
	return errors.New("coding worktree: atomic cleanup claim is unsupported on this platform")
}
