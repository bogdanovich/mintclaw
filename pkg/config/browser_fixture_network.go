package config

import (
	"errors"
	"net/url"
	"strconv"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
)

// NormalizeBrowserNetworkOrigin preserves public-only origin admission except
// for an explicit any-HTTP or narrowly scoped loopback-fixture grant.
func NormalizeBrowserNetworkOrigin(mode, raw string) (string, error) {
	switch mode {
	case BrowserNetworkAnyHTTP:
		return NormalizeBrowserHTTPOrigin(raw)
	case "", BrowserNetworkExactOrigins, BrowserNetworkPublicWeb:
		return NormalizeBrowserOrigin(raw)
	case BrowserNetworkLoopbackFixture:
		origin, err := NormalizeBrowserHTTPOrigin(raw)
		if err != nil {
			return "", errors.New("fixture origin must be canonical loopback HTTP with an unprivileged port")
		}
		parsed, err := url.Parse(origin)
		if err != nil || origin != raw || parsed.Scheme != "http" ||
			parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1" {
			return "", errors.New("fixture origin must be canonical loopback HTTP with an unprivileged port")
		}
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port <= 1023 || port > 65535 || strconv.Itoa(port) != parsed.Port() {
			return "", errors.New("fixture origin requires a canonical unprivileged port")
		}
		return origin, nil
	default:
		return "", errors.New("unsupported browser network mode")
	}
}

// ValidateLoopbackFixtureBrowserProfile is also used at the worker boundary,
// where callers must not be able to bypass public configuration validation.
// Listener ownership is deliberately not inferred from this network grant.
func ValidateLoopbackFixtureBrowserProfile(profile BrowserProfileConfig) error {
	if !profile.Enabled || profile.NetworkMode != BrowserNetworkLoopbackFixture ||
		profile.Mode != BrowserProfileEphemeral || profile.CapabilityMode != BrowserCapabilityRestricted ||
		profile.ApprovalMode != BrowserApprovalPolicy || profile.Policy == nil ||
		profile.Policy.DefaultDecision != browserpolicy.DecisionDeny || profile.Policy.Hook != nil ||
		profile.PrivilegedExecution != (BrowserExecutionConfig{}) || browserAttachedConfigured(profile.Attached) ||
		profile.DryRun == profile.AllowApprovedActions || len(profile.AllowedOrigins) != 1 {
		return errors.New(
			"fixture network requires one ephemeral restricted deny-default origin without hooks or execute",
		)
	}
	origin, err := NormalizeBrowserNetworkOrigin(profile.NetworkMode, profile.AllowedOrigins[0])
	if err != nil {
		return err
	}
	policy, err := browserpolicy.NormalizePolicy(*profile.Policy)
	if err != nil {
		return errors.New("invalid fixture action policy")
	}
	for _, rule := range policy.Rules {
		if rule.Decision != browserpolicy.DecisionDeny &&
			(len(rule.Match.Origins) != 1 || rule.Match.Origins[0] != origin) {
			return errors.New("non-deny fixture rules require the exact admitted origin")
		}
	}
	return nil
}
