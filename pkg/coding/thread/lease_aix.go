//go:build aix

package thread

import (
	"errors"
	"os"
)

func tryAcquireThreadLeaseFile(*os.File) error {
	return errors.New("coding thread leases are unsupported on AIX")
}

func releaseThreadLeaseFile(*os.File) error {
	return nil
}
