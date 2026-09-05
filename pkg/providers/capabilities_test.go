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

func TestCapabilitiesTreatsTypedNilAsUnavailable(t *testing.T) {
	var provider *GeminiProvider
	if capabilities := Capabilities(provider); capabilities != (ProviderCapabilities{}) {
		t.Fatalf("typed-nil capabilities = %+v, want empty descriptor", capabilities)
	}
}

func TestCallerMediatedToolsOptionsDisablesNativeSearchWithoutMutatingInput(t *testing.T) {
	input := map[string]any{"native_search": true, "max_tokens": 42}
	got := CallerMediatedToolsOptions(input)
	if got["native_search"] != false || got["max_tokens"] != 42 {
		t.Fatalf("caller-mediated options = %#v", got)
	}
	if input["native_search"] != true {
		t.Fatalf("input options mutated: %#v", input)
	}
}

func TestChatStreamEventsDispatchesEventProvider(t *testing.T) {
	provider := &eventTestProvider{descriptorTestProvider: descriptorTestProvider{
		capabilities: ProviderCapabilities{
			Streaming: true,
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
		Streaming: true,
	}}
	_, attempted, err := ChatStreamEvents(t.Context(), provider, nil, nil, "test", nil, nil)
	if !attempted || !errors.Is(err, ErrStreamingContract) {
		t.Fatalf("ChatStreamEvents() = attempted %v, error %v", attempted, err)
	}
}

func TestBuiltinProviderCapabilityDescriptors(t *testing.T) {
	httpProvider := NewHTTPProvider("key", "https://api.openai.com/v1", "")
	httpCapabilities := Capabilities(httpProvider)
	if !httpCapabilities.Streaming || !httpCapabilities.NativeSearch || !httpCapabilities.CallerMediatedTools {
		t.Fatalf("OpenAI HTTP capabilities = %+v", httpCapabilities)
	}

	deepSeekProvider := NewHTTPProvider("key", "https://api.deepseek.com/v1", "")
	deepSeekProvider.SetProviderName("deepseek")
	deepSeekCapabilities := Capabilities(deepSeekProvider)
	if !deepSeekCapabilities.Streaming || !deepSeekCapabilities.Thinking ||
		deepSeekCapabilities.NativeSearch || !deepSeekCapabilities.CallerMediatedTools {
		t.Fatalf("DeepSeek capabilities = %+v", deepSeekCapabilities)
	}

	geminiCapabilities := Capabilities(NewGeminiProvider("key", "", "", "", 0, nil, nil))
	if !geminiCapabilities.Streaming || !geminiCapabilities.Thinking ||
		!geminiCapabilities.CallerMediatedTools {
		t.Fatalf("Gemini capabilities = %+v", geminiCapabilities)
	}

	claudeCapabilities := Capabilities(NewClaudeProvider("token"))
	if !claudeCapabilities.Thinking || claudeCapabilities.Streaming ||
		!claudeCapabilities.CallerMediatedTools {
		t.Fatalf("Claude OAuth capabilities = %+v", claudeCapabilities)
	}

	codexCapabilities := Capabilities(NewCodexProvider("token", "account"))
	if !codexCapabilities.Thinking || !codexCapabilities.ImageGeneration.Supported ||
		codexCapabilities.ImageGeneration.MaxResults != 4 || !codexCapabilities.CallerMediatedTools {
		t.Fatalf("Codex OAuth capabilities = %+v", codexCapabilities)
	}

	cliCapabilities := Capabilities(NewCodexCliProvider("."))
	if cliCapabilities != (ProviderCapabilities{}) {
		t.Fatalf("Codex CLI capabilities = %+v", cliCapabilities)
	}
	if capabilities := Capabilities(NewClaudeCliProvider(".")); capabilities.CallerMediatedTools {
		t.Fatalf("Claude CLI capabilities = %+v", capabilities)
	}
	if capabilities := Capabilities((*GitHubCopilotProvider)(nil)); capabilities.CallerMediatedTools {
		t.Fatalf("GitHub Copilot capabilities = %+v", capabilities)
	}

	overriddenHTTP := NewHTTPProviderWithMaxTokensFieldAndRequestTimeout(
		"key", "https://api.openai.com/v1", "", "", "", 0, map[string]any{"tools": []any{}}, nil,
	)
	if capabilities := Capabilities(overriddenHTTP); capabilities.CallerMediatedTools {
		t.Fatalf("tool-overridden HTTP capabilities = %+v", capabilities)
	}
	overriddenGemini := NewGeminiProvider("key", "", "", "", 0, map[string]any{"tools": []any{}}, nil)
	if capabilities := Capabilities(overriddenGemini); capabilities.CallerMediatedTools {
		t.Fatalf("tool-overridden Gemini capabilities = %+v", capabilities)
	}

	openRouter := NewHTTPProvider("key", "https://openrouter.ai/api/v1", "")
	openRouter.SetProviderName("openrouter")
	if capabilities := Capabilities(openRouter); capabilities.CallerMediatedTools {
		t.Fatalf("OpenRouter capabilities = %+v", capabilities)
	}
	pluginHTTP := NewHTTPProviderWithMaxTokensFieldAndRequestTimeout(
		"key", "https://api.openai.com/v1", "", "", "", 0, map[string]any{"plugins": []any{}}, nil,
	)
	if capabilities := Capabilities(pluginHTTP); capabilities.CallerMediatedTools {
		t.Fatalf("hosted-plugin HTTP capabilities = %+v", capabilities)
	}
	for _, extraBody := range []map[string]any{
		{"web_search_options": map[string]any{}},
		{"enable_search": true},
		{"enable_code_interpreter": true},
	} {
		configuredHTTP := NewHTTPProviderWithMaxTokensFieldAndRequestTimeout(
			"key", "https://api.openai.com/v1", "", "", "", 0, extraBody, nil,
		)
		if capabilities := Capabilities(configuredHTTP); capabilities.CallerMediatedTools {
			t.Fatalf("extended HTTP capabilities = %+v for %#v", capabilities, extraBody)
		}
	}
	cachedGemini := NewGeminiProvider(
		"key", "", "", "", 0, map[string]any{"cachedContent": "cachedContents/review"}, nil,
	)
	if capabilities := Capabilities(cachedGemini); capabilities.CallerMediatedTools {
		t.Fatalf("cached-content Gemini capabilities = %+v", capabilities)
	}

	mutableExtraBody := map[string]any{}
	snapshottedHTTP := NewHTTPProviderWithMaxTokensFieldAndRequestTimeout(
		"key", "https://api.openai.com/v1", "", "", "", 0, mutableExtraBody, nil,
	)
	mutableExtraBody["tools"] = []any{}
	if capabilities := Capabilities(snapshottedHTTP); !capabilities.CallerMediatedTools {
		t.Fatalf("snapshotted HTTP capabilities = %+v", capabilities)
	}
}
