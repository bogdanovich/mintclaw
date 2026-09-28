//go:build !linux && !darwin

package companion

import "errors"

func normalizeAuthorityBrokerPlatformConfig(
	AuthorityBrokerConfig,
	string,
) (AuthorityBrokerConfig, error) {
	return AuthorityBrokerConfig{}, errors.New("authority broker requires Linux or macOS")
}
