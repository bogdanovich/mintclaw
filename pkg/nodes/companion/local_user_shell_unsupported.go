//go:build !linux && !darwin

package companion

import (
	"errors"
)

func normalizeLocalUserShellConfig(
	LocalUserShellConfig,
	string,
) (LocalUserShellConfig, error) {
	return LocalUserShellConfig{}, errors.New("local-user owner shell requires Linux or macOS")
}

// NewLocalUserShellBroker rejects the Unix-only executor on other platforms.
func NewLocalUserShellBroker(
	LocalUserShellConfig,
) (ShellBrokerSnapshot, ShellBroker, error) {
	return ShellBrokerSnapshot{}, nil, errors.New("local-user owner shell requires Linux or macOS")
}
