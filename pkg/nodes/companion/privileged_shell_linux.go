//go:build linux

package companion

import (
	"errors"
	"strings"
)

func normalizePrivilegedShellHelperConfig(
	config PrivilegedShellHelperConfig,
	baseDir string,
) (PrivilegedShellHelperConfig, error) {
	if strings.TrimSpace(config.Endpoint) == "" {
		return PrivilegedShellHelperConfig{}, errors.New("Linux privileged helper requires an endpoint")
	}
	endpoint, err := resolveConfigPath(baseDir, config.Endpoint)
	if err != nil {
		return PrivilegedShellHelperConfig{}, errors.New("privileged helper endpoint is invalid")
	}
	config.Endpoint = endpoint
	return config, nil
}
