//go:build windows

package outbox

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func acquireCoordinatorLock(path string) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open outbox coordinator lock: %w", err)
	}
	err = windows.LockFileEx(windows.Handle(lock.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &windows.Overlapped{})
	if err != nil {
		_ = lock.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrCoordinatorOwned
		}
		return nil, fmt.Errorf("lock outbox coordinator: %w", err)
	}
	return lock, nil
}
