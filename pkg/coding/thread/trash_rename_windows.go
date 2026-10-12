//go:build windows

package thread

import "errors"

// Windows cross-directory moves reject a directory containing an open writer
// file. Retire that one-shot lease while the existing catalog gate excludes
// all native writer admission; keep the gate until the move finishes.
// TrashThread calls this with lease.mu already held.
func (s *Store) acquireThreadTrashRenameGate(lease *Lease) (*catalogLease, error) {
	gate, err := s.acquireCatalogLease()
	if err != nil {
		return nil, err
	}
	lease.once.Do(func() {
		lease.err = errors.Join(releaseThreadLeaseFile(lease.file), lease.file.Close())
		lease.released = true
	})
	if lease.err != nil {
		return nil, errors.Join(lease.err, releaseCatalogLease(gate))
	}
	return gate, nil
}
