package providers

import (
	"context"

	"github.com/bogdanovich/mintclaw/pkg/providers/common"
)

type toolSchemaTransformProvider struct {
	delegate  LLMProvider
	transform string
}

func wrapProviderWithToolSchemaTransform(delegate LLMProvider, transform string) (LLMProvider, error) {
	transform, err := common.NormalizeToolSchemaTransform(transform)
	if err != nil {
		return nil, err
	}
	if transform == common.ToolSchemaTransformOff || delegate == nil {
		return delegate, nil
	}
	base := &toolSchemaTransformProvider{
		delegate:  delegate,
		transform: transform,
	}
	return base, nil
}

func (p *toolSchemaTransformProvider) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (*LLMResponse, error) {
	transformed, err := common.TransformToolDefinitions(tools, p.transform)
	if err != nil {
		return nil, err
	}
	return p.delegate.Chat(ctx, messages, transformed, model, options)
}

func (p *toolSchemaTransformProvider) GetDefaultModel() string {
	return p.delegate.GetDefaultModel()
}

func (p *toolSchemaTransformProvider) ChatStreamEvents(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	onChunk func(StreamChunk),
) (*LLMResponse, error) {
	transformed, err := common.TransformToolDefinitions(tools, p.transform)
	if err != nil {
		return nil, err
	}
	response, attempted, err := ChatStreamEvents(ctx, p.delegate, messages, transformed, model, options, onChunk)
	if !attempted {
		return nil, ErrStreamingContract
	}
	return response, err
}

func (p *toolSchemaTransformProvider) Capabilities() ProviderCapabilities {
	capabilities := Capabilities(p.delegate)
	if capabilities.ImageGeneration.Supported {
		if _, ok := p.delegate.(ImageGenerationProvider); !ok {
			capabilities.ImageGeneration = ImageGenerationCapabilities{}
		}
	}
	capabilities.ToolSchema = simpleToolSchemaLimits(common.MaxSimpleToolSchemaDepth)
	return capabilities
}

func (p *toolSchemaTransformProvider) GenerateImage(
	ctx context.Context,
	req ImageGenerationRequest,
) (*ImageGenerationResponse, error) {
	generator, ok := p.delegate.(ImageGenerationProvider)
	if !ok || !p.Capabilities().ImageGeneration.Supported {
		return nil, ErrImageGenerationContract
	}
	return generator.GenerateImage(ctx, req)
}

func (p *toolSchemaTransformProvider) Close() {
	if stateful, ok := p.delegate.(StatefulProvider); ok {
		stateful.Close()
	}
}
