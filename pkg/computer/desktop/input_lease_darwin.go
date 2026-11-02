//go:build darwin

package desktop

import (
	"context"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

type darwinInputLease struct {
	guard    *darwinWindow
	mu       sync.Mutex
	file     *darwinFileLease
	process  *darwinProcess
	ended    chan struct{}
	closed   bool
	closeErr error
}

func (guard *darwinWindow) acquireInputLease(ctx context.Context) (inputLeaseBackend, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !guard.alive(ctx) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, computer.ErrStale
	}
	lease := &darwinInputLease{guard: guard, ended: make(chan struct{})}
	failed := true
	defer func() {
		if failed {
			_ = lease.close()
		}
	}()
	var err error
	lease.file, err = acquireDarwinFileLease(ctx)
	if err != nil {
		return nil, err
	}
	// This watch is owned by the lease's monitor, never shared concurrently with
	// the AX thread's process handle. Revalidate the original AX guard afterward:
	// a PID reused during admission cannot replace that original incarnation.
	lease.process, err = watchDarwinProcess(guard.pid)
	if err != nil {
		return nil, err
	}
	if err = lease.check(ctx); err != nil {
		return nil, err
	}
	failed = false
	go lease.monitor()
	return lease, nil
}

func (lease *darwinInputLease) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-lease.ended:
		return computer.ErrStale
	default:
	}
	if !lease.guard.alive(ctx) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = lease.close()
		return computer.ErrStale
	}
	if !lease.mu.TryLock() {
		return computer.ErrBusy
	}
	defer lease.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !lease.validLocked() {
		_ = lease.closeLocked()
		return computer.ErrStale
	}
	return nil
}

func (lease *darwinInputLease) validLocked() bool {
	return !lease.closed && !lease.guard.lost.Load() && lease.file != nil &&
		lease.file.check() == nil && lease.process.alive()
}

func (lease *darwinInputLease) monitor() {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-lease.ended:
			return
		case <-lease.guard.loss:
			_ = lease.close()
			return
		case <-ticker.C:
			lease.mu.Lock()
			valid := lease.validLocked()
			if !valid {
				_ = lease.closeLocked()
			}
			lease.mu.Unlock()
			if !valid {
				return
			}
		}
	}
}

func (lease *darwinInputLease) done() <-chan struct{} { return lease.ended }

func (lease *darwinInputLease) close() error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.closeLocked()
}

func (lease *darwinInputLease) closeLocked() error {
	if !lease.closed {
		lease.closed = true
		close(lease.ended) // Authority revocation, not held-key/button cleanup.
		if lease.process != nil && lease.process.close() != nil {
			lease.closeErr = computer.ErrUnavailable
		}
		if lease.file.close() != nil {
			lease.closeErr = computer.ErrUnavailable
		}
	}
	return lease.closeErr
}
