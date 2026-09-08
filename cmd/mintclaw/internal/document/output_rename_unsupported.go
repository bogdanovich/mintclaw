//go:build !linux && !darwin && !windows

package document

import (
	"errors"
	"os"
)

func openOutputIdentity(path string) (*os.File, error) {
	return os.Open(path)
}

func renamePathNoReplace(_, _ string) error {
	return errors.New("atomic document output publication is unavailable on this platform")
}
