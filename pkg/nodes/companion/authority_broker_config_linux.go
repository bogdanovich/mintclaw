//go:build linux

package companion

import (
	"errors"
	"path"
	"path/filepath"
	"strings"
)

func normalizeAuthorityBrokerPlatformConfig(
	config AuthorityBrokerConfig,
	baseDir string,
) (AuthorityBrokerConfig, error) {
	if config.Companion != nil {
		return AuthorityBrokerConfig{}, errors.New("Linux authority broker cannot supervise a companion")
	}
	config.SocketPath = strings.TrimSpace(config.SocketPath)
	if config.SocketPath == "" {
		config.SocketPath = DefaultAuthorityBrokerSocket
	}
	socketPath, err := resolveAuthorityBrokerPath(baseDir, config.SocketPath, false)
	if err != nil || socketPath == string(filepath.Separator) {
		return AuthorityBrokerConfig{}, errors.New("authority broker socket path is invalid")
	}
	config.SocketPath = socketPath
	if config.AllowedUID == 0 || config.AllowedGID == 0 {
		return AuthorityBrokerConfig{}, errors.New("authority broker companion peer must be unprivileged")
	}
	config.CompanionCgroup = strings.TrimSpace(config.CompanionCgroup)
	if config.CompanionCgroup == "" ||
		len(config.CompanionCgroup) > MaxAuthorityBrokerPathBytes ||
		!strings.HasPrefix(config.CompanionCgroup, "/") ||
		config.CompanionCgroup == "/" ||
		path.Clean(config.CompanionCgroup) != config.CompanionCgroup {
		return AuthorityBrokerConfig{}, errors.New("authority broker companion cgroup is invalid")
	}
	return config, nil
}
