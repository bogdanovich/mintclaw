package providers

import (
	"context"
	"errors"

	providercapabilities "github.com/bogdanovich/mintclaw/pkg/providers/capabilities"
)

var (
	ErrStreamingContract       = errors.New("provider declares streaming without implementing a streaming operation")
	ErrImageGenerationContract = errors.New("provider declares image generation without implementing the operation")
)

// Capabilities returns the provider's normalized descriptor.
func Capabilities(provider LLMProvider) ProviderCapabilities {
	if provider == nil {
		return ProviderCapabilities{}
	}
	capable, ok := provider.(CapabilityProvider)
	if !ok {
		return ProviderCapabilities{}
	}
	return capable.Capabilities().Normalized()
}

// ImageCapabilities returns the provider's normalized image generation metadata.
func ImageCapabilities(provider ImageGenerationProvider) ImageGenerationCapabilities {
	if provider == nil {
		return ImageGenerationCapabilities{}
	}
	return provider.Capabilities().Normalized().ImageGeneration
}

// ChatStreamEvents invokes the provider's declared event-streaming operation.
// The bool reports whether streaming was declared and attempted.
func ChatStreamEvents(
	ctx context.Context,
	provider LLMProvider,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	onChunk func(StreamChunk),
) (*LLMResponse, bool, error) {
	capabilities := Capabilities(provider)
	if !capabilities.Streaming {
		return nil, false, nil
	}
	if onChunk == nil {
		onChunk = func(StreamChunk) {}
	}
	streaming, ok := provider.(StreamingProvider)
	if !ok {
		return nil, true, ErrStreamingContract
	}
	response, err := streaming.ChatStreamEvents(ctx, messages, tools, model, options, onChunk)
	return response, true, err
}

func simpleToolSchemaLimits(maxDepth int) providercapabilities.ToolSchemaLimits {
	return providercapabilities.ToolSchemaLimits{
		Transform: providercapabilities.ToolSchemaTransformSimple,
		MaxDepth:  maxDepth,
	}
}
