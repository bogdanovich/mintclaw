//go:build !unix && !windows

package config

import "errors"

func acquireConfigFileLock(string) (func(), error) {
	return nil, errors.New("configuration repository leases are unsupported on this platform")
}
