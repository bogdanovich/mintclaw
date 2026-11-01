//go:build !windows

package interactions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
)

func acquireStoreFileLock(ctx context.Context, path string) (func(), error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open interaction store lock: %w", err)
	}
	if err := retryStoreLock(ctx, func() (bool, error) {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EINTR) {
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
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}, nil
}
