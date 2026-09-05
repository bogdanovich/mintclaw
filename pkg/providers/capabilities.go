package providers

import (
	"context"
	"errors"
	"maps"
	"reflect"

	providercapabilities "github.com/bogdanovich/mintclaw/pkg/providers/capabilities"
)

var (
	ErrStreamingContract       = errors.New("provider declares streaming without implementing a streaming operation")
	ErrImageGenerationContract = errors.New("provider declares image generation without implementing the operation")
)

// Capabilities returns the provider's normalized descriptor.
func Capabilities(provider LLMProvider) ProviderCapabilities {
	if nilInterface(provider) {
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
	if nilInterface(provider) {
		return ImageGenerationCapabilities{}
	}
	return provider.Capabilities().Normalized().ImageGeneration
}

// CallerMediatedToolsOptions returns a detached request-options map that
// explicitly disables provider-owned native tools. It is required when
// CallerMediatedTools is used as a security boundary.
func CallerMediatedToolsOptions(options map[string]any) map[string]any {
	result := make(map[string]any, len(options)+1)
	maps.Copy(result, options)
	result["native_search"] = false
	return result
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
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
