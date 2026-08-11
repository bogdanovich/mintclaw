//go:build windows

package config

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func acquireConfigFileLock(path string) (func(), error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open configuration lock: %w", err)
	}
	overlapped := &windows.Overlapped{}
	if err = windows.LockFileEx(
		windows.Handle(lock.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		1,
		0,
		overlapped,
	); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("lock configuration repository: %w", err)
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(lock.Fd()), 0, 1, 0, overlapped)
		_ = lock.Close()
	}, nil
}
