//go:build unix && !aix

package config

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func acquireConfigFileLock(path string) (func(), error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open configuration lock: %w", err)
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("lock configuration repository: %w", err)
	}
	return func() {
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
	}, nil
}
