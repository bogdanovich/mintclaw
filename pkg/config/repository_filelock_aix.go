//go:build aix

package config

import "errors"

func acquireConfigFileLock(string) (func(), error) {
	return nil, errors.New("configuration repository leases are unsupported on AIX")
}
