//go:build windows

package interactions

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func acquireStoreFileLock(ctx context.Context, path string) (func(), error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open interaction store lock: %w", err)
	}
	overlapped := &windows.Overlapped{}
	if err := retryStoreLock(ctx, func() (bool, error) {
		err := windows.LockFileEx(windows.Handle(lock.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("lock interaction store: %w", err)
		}
		return true, nil
	}); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(lock.Fd()), 0, 1, 0, overlapped)
		_ = lock.Close()
	}, nil
}
