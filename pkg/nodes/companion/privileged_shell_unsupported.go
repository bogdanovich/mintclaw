//go:build !linux && !darwin

package companion

import "errors"

func normalizePrivilegedShellHelperConfig(
	PrivilegedShellHelperConfig,
	string,
) (PrivilegedShellHelperConfig, error) {
	return PrivilegedShellHelperConfig{}, errors.New("privileged owner shell requires Linux or macOS")
}
