//go:build !windows

package document

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func acquireDocumentJournalFileLock(ctx context.Context, path string) (func(), error) {
	descriptor, err := unix.Open(
		path,
		unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("open document write journal lock: %w", err)
	}
	lock := os.NewFile(uintptr(descriptor), path)
	if lock == nil {
		_ = unix.Close(descriptor)
		return nil, errors.New("open document write journal lock descriptor")
	}
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
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
	if err = validateDocumentJournalLock(path, lock); err != nil {
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
		return nil, err
	}
	return func() {
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
	}, nil
}

func validateDocumentJournalLock(path string, lock *os.File) error {
	opened, err := lock.Stat()
	if err != nil {
		return fmt.Errorf("stat open document write journal lock: %w", err)
	}
	current, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat document write journal lock: %w", err)
	}
	if !opened.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) ||
		opened.Mode().Perm()&0o077 != 0 {
		return errors.New("document write journal lock is unsafe")
	}
	return nil
}

func secureDocumentJournalRoot(path string) error {
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("document write journal root is unsafe")
	}
	return nil
}

func secureDocumentJournalRecord(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	file, err := openSourceNoFollow(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	return validateDocumentJournalRecordSecurity(path, file, info)
}

func validateDocumentJournalRecordSecurity(path string, file *os.File, opened os.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !opened.Mode().IsRegular() ||
		!os.SameFile(opened, current) || opened.Mode().Perm()&0o077 != 0 {
		return errors.New("document write journal record is unsafe")
	}
	return nil
}
