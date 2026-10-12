//go:build !linux && !darwin

package approval

func openOwnerStore(string) (ownerStore, error) { return nil, ErrUnavailable }
