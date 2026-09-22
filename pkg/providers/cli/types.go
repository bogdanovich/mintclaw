package cliprovider

import (
	"context"

	"github.com/bogdanovich/mintclaw/pkg/providers/protocoltypes"
)

type (
	ToolCall               = protocoltypes.ToolCall
	LLMResponse            = protocoltypes.LLMResponse
	UsageInfo              = protocoltypes.UsageInfo
	Message                = protocoltypes.Message
	ToolDefinition         = protocoltypes.ToolDefinition
	ToolFunctionDefinition = protocoltypes.ToolFunctionDefinition
)

type LLMProvider interface {
	Chat(
		ctx context.Context,
		messages []Message,
		tools []ToolDefinition,
		model string,
		options map[string]any,
	) (*LLMResponse, error)
	GetDefaultModel() string
}

func knownTokenCount(tokens *int) *int {
	if tokens == nil {
		return nil
	}
	return protocoltypes.KnownTokenCount(*tokens)
}

func tokenCount(tokens *int) int {
	if tokens == nil || *tokens < 0 {
		return 0
	}
	return *tokens
}
