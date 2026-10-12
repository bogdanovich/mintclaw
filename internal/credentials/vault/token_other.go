//go:build !linux && !darwin

package vault

func StoreServiceAccountToken(string, []byte) error { return ErrUnavailable }

func UseServiceAccountToken(string, func([]byte) error) error { return ErrUnavailable }
