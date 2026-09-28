package common

import (
	"net/url"
	"strings"
)

// NormalizeBaseURL ensures the Anthropic base URL is properly formatted.
// It removes a trailing /v1 suffix if present (to avoid duplication), then
// re-appends /v1 when appendV1Suffix is true. An empty apiBase falls back to
// defaultBaseURL.
func NormalizeBaseURL(apiBase, defaultBaseURL string, appendV1Suffix bool) string {
	base := strings.TrimSpace(apiBase)
	if base == "" {
		return defaultBaseURL
	}

	base = strings.TrimRight(base, "/")
	if before, ok := strings.CutSuffix(base, "/v1"); ok {
		base = before
	}
	if base == "" {
		return defaultBaseURL
	}

	if appendV1Suffix {
		return base + "/v1"
	}
	return base
}

// IsNativeAnthropicEndpoint reports whether apiBase targets Anthropic's own
// Messages API. Anthropic-compatible endpoints are not assumed to accept
// Anthropic-only cache_control fields.
func IsNativeAnthropicEndpoint(apiBase string) bool {
	parsed, err := url.Parse(strings.TrimSpace(apiBase))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), "api.anthropic.com")
}
