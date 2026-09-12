package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

var (
	errFileLockBusy = errors.New("file lock busy")
	localLockGates  sync.Map
)

type localLockGate struct {
	token chan struct{}
}

type lockedFile struct {
	file        *os.File
	unlock      func() error
	releaseGate func()
	once        sync.Once
	err         error
}

func acquireLockedFile(ctx context.Context, path string, wait bool) (*lockedFile, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	gate := localGate(path)
	releaseGate, err := gate.acquire(ctx, wait)
	if err != nil {
		return nil, err
	}
	release := true
	defer func() {
		if release {
			releaseGate()
		}
	}()

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	entry, err := os.Lstat(path)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if entry.Mode()&os.ModeSymlink != 0 || !opened.Mode().IsRegular() || !os.SameFile(entry, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("coding worktree: lock path is not a direct regular file")
	}
	for {
		var unlock func() error
		unlock, err = tryPlatformFileLock(file)
		if err == nil {
			current, statErr := os.Lstat(path)
			opened, openedErr := file.Stat()
			if statErr != nil || openedErr != nil || current.Mode()&os.ModeSymlink != 0 ||
				!opened.Mode().IsRegular() || !os.SameFile(current, opened) {
				_ = unlock()
				_ = file.Close()
				return nil, fmt.Errorf(
					"coding worktree: locked path changed during acquisition: %w",
					errors.Join(statErr, openedErr),
				)
			}
			release = false
			return &lockedFile{file: file, unlock: unlock, releaseGate: releaseGate}, nil
		}
		if !errors.Is(err, errFileLockBusy) || !wait {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, context.Cause(ctx)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (lock *lockedFile) Close() error {
	if lock == nil {
		return nil
	}
	lock.once.Do(func() {
		lock.err = errors.Join(lock.unlock(), lock.file.Close())
		lock.releaseGate()
	})
	return lock.err
}

func localGate(path string) *localLockGate {
	created := &localLockGate{token: make(chan struct{}, 1)}
	created.token <- struct{}{}
	actual, _ := localLockGates.LoadOrStore(path, created)
	return actual.(*localLockGate)
}

func (gate *localLockGate) acquire(ctx context.Context, wait bool) (func(), error) {
	if !wait {
		select {
		case <-gate.token:
			return func() { gate.token <- struct{}{} }, nil
		default:
			return nil, errFileLockBusy
		}
	}
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-gate.token:
		return func() { gate.token <- struct{}{} }, nil
	}
}
