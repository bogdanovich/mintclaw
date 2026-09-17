//go:build !unix && !windows

package thread

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The fallback targets supported by Go but not MintClaw releases retain the
// rooted path contract. They do not claim cross-process lease support.
func openThreadLeaseFile(root *catalogDirectory) (*os.File, error) {
	return openLeaseFile(root, leaseFileName)
}

func openLeaseFile(root *catalogDirectory, name string) (*os.File, error) {
	if root == nil || root.root == nil || !filepath.IsLocal(name) {
		return nil, fmt.Errorf("coding thread lease: thread directory is closed")
	}
	file, err := root.root.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("coding thread lease: lock file is not regular")
	}
	return file, nil
}

func tryAcquireThreadLeaseFile(*os.File) error {
	return errors.New("coding thread leases are unsupported on this platform")
}

func releaseThreadLeaseFile(*os.File) error {
	return nil
}
