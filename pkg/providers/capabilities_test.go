package providers

import (
	"context"
	"errors"
	"testing"
)

type descriptorTestProvider struct {
	capabilities ProviderCapabilities
}

func (p *descriptorTestProvider) Chat(
	context.Context,
	[]Message,
	[]ToolDefinition,
	string,
	map[string]any,
) (*LLMResponse, error) {
	return &LLMResponse{Content: "chat"}, nil
}

func (p *descriptorTestProvider) GetDefaultModel() string { return "test" }

func (p *descriptorTestProvider) Capabilities() ProviderCapabilities { return p.capabilities }

type eventTestProvider struct{ descriptorTestProvider }

type imageDescriptorTestProvider struct{ descriptorTestProvider }

func (p *imageDescriptorTestProvider) GenerateImage(
	context.Context,
	ImageGenerationRequest,
) (*ImageGenerationResponse, error) {
	return &ImageGenerationResponse{}, nil
}

func (p *eventTestProvider) ChatStreamEvents(
	_ context.Context,
	_ []Message,
	_ []ToolDefinition,
	_ string,
	_ map[string]any,
	onChunk func(StreamChunk),
) (*LLMResponse, error) {
	onChunk(StreamChunk{Content: "event", ReasoningContent: "reasoning"})
	return &LLMResponse{Content: "event"}, nil
}

func TestCapabilitiesUsesCurrentDescriptor(t *testing.T) {
	capabilities := Capabilities(&descriptorTestProvider{})
	if capabilities.Thinking || capabilities.NativeSearch {
		t.Fatalf("capabilities = %+v, want empty descriptor", capabilities)
	}
}

func TestImageCapabilitiesUsesCurrentDescriptor(t *testing.T) {
	capabilities := ImageCapabilities(&imageDescriptorTestProvider{})
	if capabilities != (ImageGenerationCapabilities{}) {
		t.Fatalf("image capabilities = %+v, want empty descriptor", capabilities)
	}
}

func TestChatStreamEventsDispatchesEventProvider(t *testing.T) {
	provider := &eventTestProvider{descriptorTestProvider: descriptorTestProvider{
		capabilities: ProviderCapabilities{
			Streaming: StreamingCapabilities{Supported: true, Events: true},
		},
	}}
	var got StreamChunk
	response, attempted, err := ChatStreamEvents(
		t.Context(), provider, nil, nil, "test", nil, func(chunk StreamChunk) { got = chunk },
	)
	if err != nil || !attempted {
		t.Fatalf("ChatStreamEvents() = attempted %v, error %v", attempted, err)
	}
	if response.Content != "event" || got.ReasoningContent != "reasoning" {
		t.Fatalf("response/chunk = %+v/%+v", response, got)
	}
}

func TestChatStreamEventsRejectsDescriptorOperationMismatch(t *testing.T) {
	provider := &descriptorTestProvider{capabilities: ProviderCapabilities{
		Streaming: StreamingCapabilities{Supported: true, Events: true},
	}}
	_, attempted, err := ChatStreamEvents(t.Context(), provider, nil, nil, "test", nil, nil)
	if !attempted || !errors.Is(err, ErrStreamingContract) {
		t.Fatalf("ChatStreamEvents() = attempted %v, error %v", attempted, err)
	}
}

func TestBuiltinProviderCapabilityDescriptors(t *testing.T) {
	httpProvider := NewHTTPProvider("key", "https://api.openai.com/v1", "")
	httpCapabilities := Capabilities(httpProvider)
	if !httpCapabilities.Streaming.Events || !httpCapabilities.NativeSearch {
		t.Fatalf("OpenAI HTTP capabilities = %+v", httpCapabilities)
	}

	deepSeekProvider := NewHTTPProvider("key", "https://api.deepseek.com/v1", "")
	deepSeekProvider.SetProviderName("deepseek")
	deepSeekCapabilities := Capabilities(deepSeekProvider)
	if !deepSeekCapabilities.Streaming.Events || !deepSeekCapabilities.Thinking ||
		deepSeekCapabilities.NativeSearch {
		t.Fatalf("DeepSeek capabilities = %+v", deepSeekCapabilities)
	}

	geminiCapabilities := Capabilities(NewGeminiProvider("key", "", "", "", 0, nil, nil))
	if !geminiCapabilities.Streaming.Events || !geminiCapabilities.Thinking {
		t.Fatalf("Gemini capabilities = %+v", geminiCapabilities)
	}

	claudeCapabilities := Capabilities(NewClaudeProvider("token"))
	if !claudeCapabilities.Thinking || claudeCapabilities.Streaming.Supported {
		t.Fatalf("Claude OAuth capabilities = %+v", claudeCapabilities)
	}

	codexCapabilities := Capabilities(NewCodexProvider("token", "account"))
	if !codexCapabilities.Thinking || !codexCapabilities.ImageGeneration.Supported ||
		codexCapabilities.ImageGeneration.MaxResults != 4 {
		t.Fatalf("Codex OAuth capabilities = %+v", codexCapabilities)
	}

	cliCapabilities := Capabilities(NewCodexCliProvider("."))
	if cliCapabilities != (ProviderCapabilities{}) {
		t.Fatalf("Codex CLI capabilities = %+v", cliCapabilities)
	}
}
