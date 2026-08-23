package providers

import (
	"context"
	"errors"
	"reflect"
	"testing"

	providercommon "github.com/bogdanovich/mintclaw/pkg/providers/common"
)

type toolCaptureProvider struct {
	lastTools    []ToolDefinition
	capabilities ProviderCapabilities
}

type streamingToolCaptureProvider struct{ toolCaptureProvider }

type imageToolCaptureProvider struct {
	toolCaptureProvider
	request ImageGenerationRequest
}

func (p *imageToolCaptureProvider) GenerateImage(
	_ context.Context,
	req ImageGenerationRequest,
) (*ImageGenerationResponse, error) {
	p.request = req
	return &ImageGenerationResponse{Images: []GeneratedImage{{Data: []byte("image")}}}, nil
}

func (p *streamingToolCaptureProvider) ChatStreamEvents(
	_ context.Context,
	_ []Message,
	tools []ToolDefinition,
	_ string,
	_ map[string]any,
	onChunk func(StreamChunk),
) (*LLMResponse, error) {
	p.lastTools = tools
	onChunk(StreamChunk{Content: "streamed"})
	return &LLMResponse{Content: "streamed"}, nil
}

func (p *toolCaptureProvider) Chat(
	_ context.Context,
	_ []Message,
	tools []ToolDefinition,
	_ string,
	_ map[string]any,
) (*LLMResponse, error) {
	p.lastTools = tools
	return &LLMResponse{Content: "ok"}, nil
}

func (p *toolCaptureProvider) GetDefaultModel() string {
	return "test"
}

func (p *toolCaptureProvider) Capabilities() ProviderCapabilities { return p.capabilities }

func TestWrapProviderWithToolSchemaTransform_DisabledPassesToolsThrough(t *testing.T) {
	capture := &toolCaptureProvider{}
	wrapped, err := wrapProviderWithToolSchemaTransform(capture, "")
	if err != nil {
		t.Fatalf("wrapProviderWithToolSchemaTransform() error = %v", err)
	}

	tools := []ToolDefinition{{
		Type: "function",
		Function: ToolFunctionDefinition{
			Name:       "noop",
			Parameters: map[string]any{"type": "object"},
		},
	}}

	_, err = wrapped.Chat(t.Context(), nil, tools, "test", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if !reflect.DeepEqual(capture.lastTools, tools) {
		t.Fatalf("tools mutated with transform off\n got: %#v\nwant: %#v", capture.lastTools, tools)
	}
}

func TestWrapProviderWithToolSchemaTransform_GoogleSanitizesSchemas(t *testing.T) {
	capture := &toolCaptureProvider{capabilities: ProviderCapabilities{
		Thinking:     true,
		NativeSearch: true,
	}}
	wrapped, err := wrapProviderWithToolSchemaTransform(capture, "simple")
	if err != nil {
		t.Fatalf("wrapProviderWithToolSchemaTransform() error = %v", err)
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"parent": map[string]any{
				"anyOf": []any{
					map[string]any{"$ref": "#/$defs/pageParent"},
					map[string]any{"$ref": "#/$defs/databaseParent"},
				},
			},
		},
		"$defs": map[string]any{
			"pageParent": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"page_id": map[string]any{"type": "string"},
				},
			},
			"databaseParent": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"database_id": map[string]any{"type": "string"},
				},
			},
		},
	}
	tools := []ToolDefinition{{
		Type: "function",
		Function: ToolFunctionDefinition{
			Name:       "mcp_notion_create",
			Parameters: schema,
		},
	}}

	_, err = wrapped.Chat(t.Context(), nil, tools, "test", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	want := providercommon.SanitizeSchemaForGoogle(schema)
	got := capture.lastTools[0].Function.Parameters
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitized parameters mismatch\n got: %#v\nwant: %#v", got, want)
	}
	capabilities := Capabilities(wrapped)
	if !capabilities.Thinking || !capabilities.NativeSearch {
		t.Fatalf("wrapper dropped delegate capabilities: %+v", capabilities)
	}
	if capabilities.ToolSchema.Transform != providercommon.ToolSchemaTransformSimple ||
		capabilities.ToolSchema.MaxDepth != providercommon.MaxSimpleToolSchemaDepth {
		t.Fatalf("tool schema limits = %+v", capabilities.ToolSchema)
	}
}

func TestToolSchemaTransformWrapperStreamsWithDeclaredCapabilities(t *testing.T) {
	capture := &streamingToolCaptureProvider{toolCaptureProvider: toolCaptureProvider{
		capabilities: ProviderCapabilities{
			Streaming: true,
		},
	}}
	wrapped, err := wrapProviderWithToolSchemaTransform(capture, "simple")
	if err != nil {
		t.Fatalf("wrapProviderWithToolSchemaTransform() error = %v", err)
	}
	tools := []ToolDefinition{{
		Type: "function",
		Function: ToolFunctionDefinition{
			Name: "lookup",
			Parameters: map[string]any{
				"type":  "object",
				"$defs": map[string]any{"unused": map[string]any{"type": "string"}},
			},
		},
	}}
	var chunk StreamChunk
	response, attempted, err := ChatStreamEvents(
		t.Context(), wrapped, nil, tools, "test", nil, func(value StreamChunk) { chunk = value },
	)
	if err != nil || !attempted {
		t.Fatalf("ChatStreamEvents() = attempted %v, error %v", attempted, err)
	}
	if response.Content != "streamed" || chunk.Content != "streamed" {
		t.Fatalf("response/chunk = %+v/%+v", response, chunk)
	}
	if _, ok := capture.lastTools[0].Function.Parameters["$defs"]; ok {
		t.Fatalf("streaming schema was not transformed: %+v", capture.lastTools)
	}
}

func TestToolSchemaTransformWrapperPreservesAdvertisedImageOperation(t *testing.T) {
	capture := &imageToolCaptureProvider{toolCaptureProvider: toolCaptureProvider{
		capabilities: ProviderCapabilities{ImageGeneration: ImageGenerationCapabilities{
			Supported:    true,
			ProviderID:   "image-capture",
			DefaultModel: "image-default",
			MaxResults:   2,
		}},
	}}
	wrapped, err := wrapProviderWithToolSchemaTransform(capture, "simple")
	if err != nil {
		t.Fatalf("wrapProviderWithToolSchemaTransform() error = %v", err)
	}
	imageProvider, ok := wrapped.(ImageGenerationProvider)
	if !ok {
		t.Fatalf("wrapped provider %T does not preserve image generation", wrapped)
	}
	response, err := imageProvider.GenerateImage(t.Context(), ImageGenerationRequest{Prompt: "icon"})
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	if len(response.Images) != 1 || capture.request.Prompt != "icon" {
		t.Fatalf("response/request = %+v/%+v", response, capture.request)
	}
}

func TestToolSchemaTransformWrapperClearsImageCapabilityWithoutOperation(t *testing.T) {
	capture := &toolCaptureProvider{capabilities: ProviderCapabilities{
		ImageGeneration: ImageGenerationCapabilities{Supported: true, ProviderID: "mismatch"},
	}}
	wrapped, err := wrapProviderWithToolSchemaTransform(capture, "simple")
	if err != nil {
		t.Fatalf("wrapProviderWithToolSchemaTransform() error = %v", err)
	}
	if Capabilities(wrapped).ImageGeneration.Supported {
		t.Fatalf("wrapper advertised missing image operation: %+v", Capabilities(wrapped))
	}
	imageProvider := wrapped.(ImageGenerationProvider)
	if _, err := imageProvider.GenerateImage(t.Context(), ImageGenerationRequest{}); !errors.Is(
		err,
		ErrImageGenerationContract,
	) {
		t.Fatalf("GenerateImage() error = %v, want ErrImageGenerationContract", err)
	}
}
