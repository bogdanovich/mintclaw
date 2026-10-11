//go:build !linux

package approval

func openOwnerStore(string) (ownerStore, error) { return nil, ErrUnavailable }
