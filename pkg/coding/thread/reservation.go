package thread

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrThreadExists classifies an exact new-thread reservation that would reuse
// any pre-existing thread directory, including unpublished partial state.
var ErrThreadExists = errors.New("coding thread already exists")

// ReserveThreadLease durably reserves a new thread and returns its first
// writer lease without exposing an unlocked active directory.
func (s *Store) ReserveThreadLease(threadID string) (*Lease, error) {
	threadsRoot, threadRoot, lease, err := s.reservePinnedThreadLease(threadID)
	if err != nil {
		return nil, err
	}
	if closeErr := errors.Join(threadRoot.Close(), threadsRoot.Close()); closeErr != nil {
		return nil, fmt.Errorf(
			"coding thread store: close new thread reservation: %w",
			errors.Join(closeErr, lease.Release()),
		)
	}
	return lease, nil
}

func (s *Store) reservePinnedThreadLease(threadID string) (*os.Root, *os.Root, *Lease, error) {
	catalogLease, err := s.acquireCatalogLease()
	if err != nil {
		return nil, nil, nil, err
	}
	threadsRoot, threadRoot, err := s.reserveThreadDirectory(threadID)
	if err != nil {
		return nil, nil, nil, errors.Join(err, releaseCatalogLease(catalogLease))
	}
	if s.afterThreadReservation != nil {
		s.afterThreadReservation()
	}
	activePath := filepath.Join(s.root, "threads", threadID)
	lease, acquireErr := s.acquirePinnedThreadLease(threadRoot, activePath, threadID)
	releaseErr := releaseCatalogLease(catalogLease)
	if acquireErr != nil || releaseErr != nil {
		if lease != nil {
			releaseErr = errors.Join(releaseErr, lease.Release())
		}
		closeErr := errors.Join(threadRoot.Close(), threadsRoot.Close())
		return nil, nil, nil, fmt.Errorf(
			"coding thread store: acquire new thread lease; reservation left in place: %w",
			errors.Join(acquireErr, releaseErr, closeErr),
		)
	}
	return threadsRoot, threadRoot, lease, nil
}

func (s *Store) reserveThreadDirectory(threadID string) (*os.Root, *os.Root, error) {
	if s == nil {
		return nil, nil, fmt.Errorf("coding thread store is nil")
	}
	if err := validateThreadID(threadID); err != nil {
		return nil, nil, err
	}
	threadsPath := filepath.Join(s.root, "threads")
	relativeThreads, err := filepath.Rel(s.durableRoot, threadsPath)
	if err != nil {
		return nil, nil, fmt.Errorf("coding thread store: resolve threads root: %w", err)
	}
	if !filepath.IsLocal(relativeThreads) {
		return nil, nil, fmt.Errorf("coding thread store: threads root escapes durable store")
	}
	if mkdirErr := s.mkdirDurable(s.durableRoot, relativeThreads, 0o700); mkdirErr != nil {
		return nil, nil, fmt.Errorf("coding thread store: create threads root: %w", mkdirErr)
	}
	threadsRoot, err := openPinnedCatalogRoot(threadsPath)
	if err != nil {
		return nil, nil, fmt.Errorf("coding thread store: pin threads root: %w", err)
	}
	if mkdirErr := threadsRoot.Mkdir(threadID, 0o700); mkdirErr != nil {
		_ = threadsRoot.Close()
		if os.IsExist(mkdirErr) {
			return nil, nil, fmt.Errorf("%w: %q", ErrThreadExists, threadID)
		}
		return nil, nil, fmt.Errorf("coding thread store: reserve new thread: %w", mkdirErr)
	}
	threadRoot, err := threadsRoot.OpenRoot(threadID)
	if err != nil {
		_ = threadsRoot.Close()
		return nil, nil, fmt.Errorf("coding thread store: pin new thread reservation: %w", err)
	}
	if err := s.syncRoot(threadsRoot); err != nil {
		cleanupErr := cleanupThreadReservation(threadsRoot, threadRoot, threadID)
		return nil, nil, errors.Join(
			fmt.Errorf("coding thread store: sync new thread reservation: %w", err),
			cleanupErr,
			threadRoot.Close(),
			threadsRoot.Close(),
		)
	}
	return threadsRoot, threadRoot, nil
}

func cleanupThreadReservation(threadsRoot, threadRoot *os.Root, threadID string) error {
	quarantineName := ".thread-reservation-" + NewThreadID()
	if err := threadsRoot.Rename(threadID, quarantineName); err != nil {
		return err
	}
	restore := true
	defer func() {
		if restore {
			_ = threadsRoot.Rename(quarantineName, threadID)
		}
	}()
	active, err := threadsRoot.Lstat(quarantineName)
	if err != nil {
		return err
	}
	pinned, err := threadRoot.Open(".")
	if err != nil {
		return err
	}
	pinnedInfo, statErr := pinned.Stat()
	closeErr := pinned.Close()
	if err := errors.Join(statErr, closeErr); err != nil {
		return err
	}
	if active.Mode()&os.ModeSymlink != 0 || !os.SameFile(active, pinnedInfo) {
		return fmt.Errorf("coding thread store: reservation identity changed")
	}
	if err := threadsRoot.Remove(quarantineName); err != nil {
		return err
	}
	restore = false
	return syncRootDirectory(threadsRoot)
}
