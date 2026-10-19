package interactions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StoreBusyError is a retryable admission failure. No durable mutation has
// started. Cause distinguishes cancellation from an exhausted time budget.
type StoreBusyError struct{ Cause error }

func (e *StoreBusyError) Error() string        { return fmt.Sprintf("%s: %v", ErrStoreBusy, e.Cause) }
func (e *StoreBusyError) Unwrap() error        { return e.Cause }
func (e *StoreBusyError) Is(target error) bool { return target == ErrStoreBusy }

// retryStoreLock never starts a blocking lock syscall or a detached goroutine.
func retryStoreLock(ctx context.Context, try func() (bool, error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return &StoreBusyError{Cause: err}
		}
		acquired, err := try()
		if err != nil {
			return err
		}
		if acquired {
			return nil
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return &StoreBusyError{Cause: ctx.Err()}
		case <-timer.C:
		}
	}
}

// lockAndReload returns with mu held only on success. All writers acquire the
// file lock before mu, so a live external holder cannot block reads, observer
// removal, or other writers' independent admission deadlines.
func (r *Registry) lockAndReload(ctx context.Context) (func(), error) {
	if r.closed.Load() {
		return nil, ErrStoreClosed
	}
	ctx, cancel := context.WithTimeout(ctx, r.options.StoreLockTimeout)
	defer cancel()
	release := func() {}
	if r.storePath != "" {
		if err := os.MkdirAll(filepath.Dir(r.storePath), 0o700); err != nil {
			return nil, fmt.Errorf("create interaction store directory: %w", err)
		}
		var err error
		release, err = acquireStoreFileLock(ctx, r.storePath+".lock")
		if err != nil {
			return nil, err
		}
	}
	if err := retryStoreLock(ctx, func() (bool, error) { return r.mu.TryLock(), nil }); err != nil {
		release()
		return nil, err
	}
	if r.closed.Load() {
		r.mu.Unlock()
		release()
		return nil, ErrStoreClosed
	}
	if r.loadErr != nil && !errors.Is(r.loadErr, ErrStoreBusy) {
		err := fmt.Errorf("%w: %w", ErrStoreUnavailable, r.loadErr)
		r.mu.Unlock()
		release()
		return nil, err
	}
	if r.storePath != "" {
		before := r.snapshotLocked()
		r.records = make(map[string]Record)
		r.events = nil
		r.commitSequence = 0
		if err := r.load(); err != nil {
			r.restoreSnapshotLocked(before)
			r.mu.Unlock()
			release()
			return nil, fmt.Errorf("%w: reload under lock: %w", ErrStoreUnavailable, err)
		}
	}
	r.loadErr = nil
	return release, nil
}

// Refresh retries a transient startup admission failure and reloads durable
// state without mutating it. A failed/canceled refresh preserves cached state.
func (r *Registry) Refresh(ctx context.Context) error {
	if r == nil {
		return ErrStoreUnavailable
	}
	release, err := r.lockAndReload(ctx)
	if err != nil {
		return err
	}
	release()
	r.mu.Unlock()
	return nil
}
