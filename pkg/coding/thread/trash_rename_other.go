//go:build !windows

package thread

func (s *Store) acquireThreadTrashRenameGate(_ *Lease) (*catalogLease, error) {
	return nil, nil
}
