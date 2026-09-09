package thread

import (
	"errors"
	"fmt"
	"io/fs"
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
	// The published thread lock, not these preparation handles, now owns
	// writer authority. A close error must not turn success into an unlocked
	// partial thread, so cleanup is deliberately best-effort here.
	_ = s.closeReservationRoots(threadRoot, threadsRoot)
	return lease, nil
}

func (s *Store) reservePinnedThreadLease(threadID string) (*os.Root, *os.Root, *Lease, error) {
	catalogLease, err := s.acquireCatalogLease()
	if err != nil {
		return nil, nil, nil, err
	}
	threadsRoot, threadRoot, stagingName, err := s.prepareThreadReservation(threadID)
	if err != nil {
		return nil, nil, nil, errors.Join(err, releaseCatalogLease(catalogLease))
	}
	abort := func(activeName string, lease *Lease, operationErr error) (*os.Root, *os.Root, *Lease, error) {
		cleanupErr := s.quarantineThreadReservation(threadsRoot, threadRoot, activeName, lease)
		closeErr := errors.Join(threadRoot.Close(), threadsRoot.Close())
		releaseErr := releaseCatalogLease(catalogLease)
		return nil, nil, nil, errors.Join(operationErr, cleanupErr, closeErr, releaseErr)
	}
	if s.afterThreadReservationPrepared != nil {
		s.afterThreadReservationPrepared(stagingName)
	}
	stagingPath := filepath.Join(s.root, "threads", stagingName)
	lease, acquireErr := s.acquirePreparedThreadLease(threadRoot, stagingPath, threadID)
	if acquireErr != nil {
		return abort(stagingName, nil, fmt.Errorf("coding thread store: acquire prepared thread lease: %w", acquireErr))
	}
	if syncErr := s.syncRoot(threadRoot); syncErr != nil {
		return abort(
			stagingName,
			lease,
			fmt.Errorf("coding thread store: sync prepared thread: %w", syncErr),
		)
	}
	if publishErr := renameThreadReservationNoReplace(threadsRoot, stagingName, threadID); publishErr != nil {
		if errors.Is(publishErr, fs.ErrExist) {
			return abort(stagingName, lease, fmt.Errorf("%w: %q", ErrThreadExists, threadID))
		}
		return abort(
			stagingName,
			lease,
			fmt.Errorf("coding thread store: publish new thread reservation: %w", publishErr),
		)
	}
	if s.afterThreadReservationPublished != nil {
		s.afterThreadReservationPublished()
	}
	activePath := filepath.Join(s.root, "threads", threadID)
	if validationErr := validatePinnedThreadReservation(threadRoot, activePath); validationErr != nil {
		return abort(
			threadID,
			lease,
			fmt.Errorf("coding thread store: revalidate published thread: %w", validationErr),
		)
	}
	if validationErr := s.validateAcquiredLeasePath(threadID, lease.file); validationErr != nil {
		return abort(
			threadID,
			lease,
			fmt.Errorf("coding thread store: revalidate published thread lease: %w", validationErr),
		)
	}
	if syncErr := s.syncRoot(threadsRoot); syncErr != nil {
		return abort(
			threadID,
			lease,
			fmt.Errorf("coding thread store: sync published thread reservation: %w", syncErr),
		)
	}
	if releaseErr := releaseCatalogLease(catalogLease); releaseErr != nil {
		cleanupErr := s.quarantineThreadReservation(threadsRoot, threadRoot, threadID, lease)
		closeErr := errors.Join(threadRoot.Close(), threadsRoot.Close())
		return nil, nil, nil, errors.Join(releaseErr, cleanupErr, closeErr)
	}
	return threadsRoot, threadRoot, lease, nil
}

func (s *Store) prepareThreadReservation(threadID string) (*os.Root, *os.Root, string, error) {
	if s == nil {
		return nil, nil, "", fmt.Errorf("coding thread store is nil")
	}
	if err := validateThreadID(threadID); err != nil {
		return nil, nil, "", err
	}
	threadsPath := filepath.Join(s.root, "threads")
	relativeThreads, err := filepath.Rel(s.durableRoot, threadsPath)
	if err != nil {
		return nil, nil, "", fmt.Errorf("coding thread store: resolve threads root: %w", err)
	}
	if !filepath.IsLocal(relativeThreads) {
		return nil, nil, "", fmt.Errorf("coding thread store: threads root escapes durable store")
	}
	if mkdirErr := s.mkdirDurable(s.durableRoot, relativeThreads, 0o700); mkdirErr != nil {
		return nil, nil, "", fmt.Errorf("coding thread store: create threads root: %w", mkdirErr)
	}
	threadsRoot, err := openPinnedCatalogRoot(threadsPath)
	if err != nil {
		return nil, nil, "", fmt.Errorf("coding thread store: pin threads root: %w", err)
	}
	stagingName := ".thread-reservation-" + NewThreadID()
	if mkdirErr := threadsRoot.Mkdir(stagingName, 0o700); mkdirErr != nil {
		_ = threadsRoot.Close()
		return nil, nil, "", fmt.Errorf("coding thread store: prepare new thread: %w", mkdirErr)
	}
	threadRoot, err := threadsRoot.OpenRoot(stagingName)
	if err != nil {
		cleanupErr := errors.Join(threadsRoot.RemoveAll(stagingName), s.syncRoot(threadsRoot))
		_ = threadsRoot.Close()
		return nil, nil, "", errors.Join(
			fmt.Errorf("coding thread store: pin prepared thread: %w", err),
			cleanupErr,
		)
	}
	return threadsRoot, threadRoot, stagingName, nil
}

func validatePinnedThreadReservation(root *os.Root, activePath string) error {
	if root == nil {
		return fmt.Errorf("coding thread store: pinned thread reservation is required")
	}
	pinned, err := root.Open(".")
	if err != nil {
		return err
	}
	pinnedInfo, statErr := pinned.Stat()
	closeErr := pinned.Close()
	if joinedErr := errors.Join(statErr, closeErr); joinedErr != nil {
		return joinedErr
	}
	active, err := openCatalogRoot(activePath)
	if err != nil {
		return err
	}
	activeInfo, statErr := active.stat()
	closeErr = active.Close()
	if joinedErr := errors.Join(statErr, closeErr); joinedErr != nil {
		return joinedErr
	}
	if !os.SameFile(pinnedInfo, activeInfo) {
		return fmt.Errorf("active thread reservation no longer matches its pinned directory")
	}
	return nil
}

func (s *Store) quarantineThreadReservation(
	threadsRoot, threadRoot *os.Root,
	activeName string,
	lease *Lease,
) error {
	quarantineName := ".thread-quarantine-" + NewThreadID()
	renameErr := renameThreadReservationNoReplace(threadsRoot, activeName, quarantineName)
	if renameErr != nil {
		if lease != nil {
			renameErr = errors.Join(renameErr, lease.Release())
		}
		return fmt.Errorf("coding thread store: quarantine failed reservation: %w", renameErr)
	}
	quarantinePath := filepath.Join(s.root, "threads", quarantineName)
	identityErr := validatePinnedThreadReservation(threadRoot, quarantinePath)
	var releaseErr error
	if lease != nil {
		releaseErr = lease.Release()
	}
	if identityErr != nil {
		return errors.Join(
			fmt.Errorf("coding thread store: verify quarantined reservation: %w", identityErr),
			releaseErr,
		)
	}
	removeErr := threadsRoot.RemoveAll(quarantineName)
	syncErr := s.syncRoot(threadsRoot)
	if joinedErr := errors.Join(releaseErr, removeErr, syncErr); joinedErr != nil {
		return fmt.Errorf("coding thread store: remove quarantined reservation: %w", joinedErr)
	}
	return nil
}
