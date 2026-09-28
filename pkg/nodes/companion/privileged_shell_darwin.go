//go:build darwin

package companion

import (
	"errors"
	"strings"
)

func normalizePrivilegedShellHelperConfig(
	config PrivilegedShellHelperConfig,
	_ string,
) (PrivilegedShellHelperConfig, error) {
	if strings.TrimSpace(config.Endpoint) != "" {
		return PrivilegedShellHelperConfig{}, errors.New(
			"macOS privileged helper endpoint is supplied by the supervising LaunchDaemon",
		)
	}
	return PrivilegedShellHelperConfig{}, nil
}
