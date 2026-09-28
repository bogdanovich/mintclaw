//go:build darwin

package companion

import (
	"errors"
	"os"
	"slices"
	"strings"
)

func normalizeAuthorityBrokerPlatformConfig(
	config AuthorityBrokerConfig,
	baseDir string,
) (AuthorityBrokerConfig, error) {
	if strings.TrimSpace(config.SocketPath) != "" || config.AllowedUID != 0 ||
		config.AllowedGID != 0 || strings.TrimSpace(config.CompanionCgroup) != "" {
		return AuthorityBrokerConfig{}, errors.New("macOS authority broker rejects Linux peer configuration")
	}
	if config.Companion == nil || config.Companion.UID == 0 || config.Companion.GID == 0 ||
		len(config.Companion.SupplementaryGroups) > MaxAuthorityBrokerGroups {
		return AuthorityBrokerConfig{}, errors.New("macOS authority broker companion identity is invalid")
	}
	companion := *config.Companion
	executable, err := resolveAuthorityBrokerPath(baseDir, companion.ExecutablePath, true)
	if err != nil {
		return AuthorityBrokerConfig{}, errors.New("macOS authority broker companion executable is invalid")
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return AuthorityBrokerConfig{}, errors.New("macOS authority broker companion executable is not executable")
	}
	configPath, err := resolveAuthorityBrokerPath(baseDir, companion.ConfigPath, true)
	if err != nil {
		return AuthorityBrokerConfig{}, errors.New("macOS authority broker companion config is invalid")
	}
	info, err = os.Stat(configPath)
	if err != nil || !info.Mode().IsRegular() {
		return AuthorityBrokerConfig{}, errors.New("macOS authority broker companion config is not a regular file")
	}
	groups := append([]uint32(nil), companion.SupplementaryGroups...)
	slices.Sort(groups)
	for index, group := range groups {
		if group == 0 || (index > 0 && group == groups[index-1]) {
			return AuthorityBrokerConfig{}, errors.New("macOS authority broker companion groups are invalid")
		}
	}
	companion.ExecutablePath = executable
	companion.ConfigPath = configPath
	companion.SupplementaryGroups = groups
	config.Companion = &companion
	return config, nil
}
