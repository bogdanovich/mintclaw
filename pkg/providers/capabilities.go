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

// Capabilities returns a normalized descriptor. The structural fallbacks are
// isolated compatibility for external providers that predate CapabilityProvider.
func Capabilities(provider LLMProvider) ProviderCapabilities {
	if provider == nil {
		return ProviderCapabilities{}
	}
	if capable, ok := provider.(CapabilityProvider); ok {
		return capable.Capabilities().Normalized()
	}

	capabilities := ProviderCapabilities{}
	if _, ok := provider.(StreamingProvider); ok {
		capabilities.Streaming.Supported = true
	}
	if _, ok := provider.(StreamingEventProvider); ok {
		capabilities.Streaming = StreamingCapabilities{Supported: true, Events: true}
	}
	if capable, ok := provider.(interface{ SupportsThinking() bool }); ok {
		capabilities.Thinking = capable.SupportsThinking()
	}
	if capable, ok := provider.(interface{ SupportsNativeSearch() bool }); ok {
		capabilities.NativeSearch = capable.SupportsNativeSearch()
	}
	return capabilities.Normalized()
}

// ImageCapabilities returns descriptor-first image generation metadata while
// preserving the legacy external provider contract at one compatibility edge.
func ImageCapabilities(provider ImageGenerationCapable) ImageGenerationCapabilities {
	if provider == nil {
		return ImageGenerationCapabilities{}
	}
	if capable, ok := provider.(CapabilityProvider); ok {
		return capable.Capabilities().Normalized().ImageGeneration
	}
	if !provider.SupportsImageGeneration() {
		return ImageGenerationCapabilities{}
	}
	return ImageGenerationCapabilities{
		Supported:    true,
		ProviderID:   provider.ImageGenerationProviderID(),
		DefaultModel: provider.DefaultImageGenerationModel(),
	}
}

// ChatStreamEvents invokes the provider's declared streaming operation and
// adapts legacy accumulated-text streaming to event chunks. The bool reports
// whether streaming was declared and attempted.
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
	if !capabilities.Streaming.Supported {
		return nil, false, nil
	}
	if onChunk == nil {
		onChunk = func(StreamChunk) {}
	}
	if capabilities.Streaming.Events {
		streaming, ok := provider.(StreamingEventProvider)
		if !ok {
			return nil, true, ErrStreamingContract
		}
		response, err := streaming.ChatStreamEvents(ctx, messages, tools, model, options, onChunk)
		return response, true, err
	}
	streaming, ok := provider.(StreamingProvider)
	if !ok {
		return nil, true, ErrStreamingContract
	}
	response, err := streaming.ChatStream(ctx, messages, tools, model, options, func(accumulated string) {
		if onChunk != nil {
			onChunk(StreamChunk{Content: accumulated})
		}
	})
	return response, true, err
}

func simpleToolSchemaLimits(maxDepth int) providercapabilities.ToolSchemaLimits {
	return providercapabilities.ToolSchemaLimits{
		Transform: providercapabilities.ToolSchemaTransformSimple,
		MaxDepth:  maxDepth,
	}
}
