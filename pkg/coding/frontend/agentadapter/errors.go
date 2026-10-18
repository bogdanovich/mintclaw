package agentadapter

import (
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/agent"
)

// Error messages are selected from runtime-owned classifications, never from
// provider text, request IDs or arbitrary route names that may contain secrets.
func agentErrorMessage(payload agent.ErrorPayload, stage string) string {
	if stage != "llm" {
		return "agent error during " + stage
	}
	if payload.ProviderErrorKind == "authentication" || payload.ErrorCode == "auth" {
		return authenticationErrorMessage(payload.Provider, payload.AuthMethod)
	}
	switch payload.ProviderErrorKind {
	case "billing":
		return "The model provider rejected the request for billing reasons. Check your balance and billing settings."
	case "rate_limit":
		return "The model provider's rate limit was reached. Wait before retrying your request."
	case "timeout":
		return "The model request timed out. Check the connection and provider status before retrying."
	case "network":
		return "Could not connect to the model provider. Check the network connection and configured endpoint."
	case "context_overflow":
		return "The model's context limit was exceeded. Compact this session or select a model with a larger context."
	case "invalid_request":
		return "The model provider rejected the request. Check the selected model and its configuration."
	default:
		return "agent error during " + stage
	}
}

func authenticationErrorMessage(provider, authMethod string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	authMethod = strings.ToLower(strings.TrimSpace(authMethod))
	var name, login string
	switch provider {
	case "openai":
		name = "OpenAI"
		if authMethod == "oauth" || authMethod == "token" {
			login = "mintclaw auth login --provider openai --device-code"
		}
	case "anthropic":
		name = "Anthropic"
		if authMethod == "oauth" || authMethod == "token" {
			login = "mintclaw auth login --provider anthropic"
		}
	case "antigravity", "google-antigravity":
		name = "Google Antigravity"
		login = "mintclaw auth login --provider google-antigravity"
	default:
		return "Authentication failed. Check this model's API key, OAuth login, and provider permissions."
	}
	if login != "" {
		return name + " authentication failed. Sign in again:\n\n  " + login +
			"\n\nThen retry your request in this session."
	}
	return name + " authentication failed. Check the API key and provider permissions configured for this model."
}
