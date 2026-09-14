//go:build windows

package document

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func acquireDocumentJournalFileLock(ctx context.Context, path string) (func(), error) {
	before, statErr := os.Lstat(path)
	if statErr == nil && before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("document write journal lock is unsafe")
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("stat document write journal lock: %w", statErr)
	}
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open document write journal lock: %w", err)
	}
	overlapped := &windows.Overlapped{}
	for {
		err = windows.LockFileEx(
			windows.Handle(lock.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			overlapped,
		)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			_ = lock.Close()
			return nil, fmt.Errorf("lock document write journal: %w", err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			_ = lock.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	opened, statErr := lock.Stat()
	current, pathErr := os.Lstat(path)
	if statErr != nil || pathErr != nil || !opened.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(opened, current) || opened.Mode().Perm()&0o077 != 0 {
		_ = windows.UnlockFileEx(windows.Handle(lock.Fd()), 0, 1, 0, overlapped)
		_ = lock.Close()
		return nil, errors.New("document write journal lock is unsafe")
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(lock.Fd()), 0, 1, 0, overlapped)
		_ = lock.Close()
	}, nil
}
