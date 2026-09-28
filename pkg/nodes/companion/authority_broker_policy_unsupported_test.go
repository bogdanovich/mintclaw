//go:build !linux && !darwin

package companion

import "testing"

// Unsupported platforms need the common policy tests to compile even though
// platform normalization always rejects the resulting fixture at runtime.
func validAuthorityBrokerPlatformConfig(*testing.T, string) AuthorityBrokerConfig {
	return AuthorityBrokerConfig{}
}
