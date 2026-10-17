//go:build !windows

package outbox

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func acquireCoordinatorLock(path string) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open outbox coordinator lock: %w", err)
	}
	if err = lock.Chmod(0o600); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("secure outbox coordinator lock: %w", err)
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrCoordinatorOwned
		}
		return nil, fmt.Errorf("lock outbox coordinator: %w", err)
	}
	return lock, nil
}
