//go:build !darwin

package companion

import (
	"errors"
)

func normalizeLocalUserShellConfig(
	LocalUserShellConfig,
	string,
) (LocalUserShellConfig, error) {
	return LocalUserShellConfig{}, errors.New("local-user owner shell requires macOS")
}

// NewLocalUserShellBroker rejects the macOS-only executor on other platforms.
func NewLocalUserShellBroker(
	LocalUserShellConfig,
) (ShellBrokerSnapshot, ShellBroker, error) {
	return ShellBrokerSnapshot{}, nil, errors.New("local-user owner shell requires macOS")
}
