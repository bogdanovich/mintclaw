package providers

import (
	anthropicprovider "github.com/bogdanovich/mintclaw/pkg/providers/anthropic"
	anthropicmessages "github.com/bogdanovich/mintclaw/pkg/providers/anthropic_messages"
	"github.com/bogdanovich/mintclaw/pkg/providers/azure"
	"github.com/bogdanovich/mintclaw/pkg/providers/bedrock"
	"github.com/bogdanovich/mintclaw/pkg/providers/openai_compat"
)

var (
	_ CapabilityProvider      = (*HTTPProvider)(nil)
	_ CapabilityProvider      = (*GeminiProvider)(nil)
	_ CapabilityProvider      = (*ClaudeProvider)(nil)
	_ CapabilityProvider      = (*CodexProvider)(nil)
	_ ImageGenerationProvider = (*CodexProvider)(nil)
	_ CapabilityProvider      = (*AntigravityProvider)(nil)
	_ CapabilityProvider      = (*ClaudeCliProvider)(nil)
	_ CapabilityProvider      = (*CodexCliProvider)(nil)
	_ CapabilityProvider      = (*GitHubCopilotProvider)(nil)
	_ CapabilityProvider      = (*anthropicprovider.Provider)(nil)
	_ CapabilityProvider      = (*anthropicmessages.Provider)(nil)
	_ CapabilityProvider      = (*azure.Provider)(nil)
	_ CapabilityProvider      = (*bedrock.Provider)(nil)
	_ CapabilityProvider      = (*openai_compat.Provider)(nil)
)
