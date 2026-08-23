package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type fakeImageGenerationProvider struct {
	id           string
	defaultModel string
	maxResults   int
	request      providers.ImageGenerationRequest
}

func (p *fakeImageGenerationProvider) Capabilities() providers.ProviderCapabilities {
	maxResults := p.maxResults
	if maxResults == 0 {
		maxResults = 4
	}
	return providers.ProviderCapabilities{ImageGeneration: providers.ImageGenerationCapabilities{
		Supported:    true,
		ProviderID:   p.id,
		DefaultModel: p.defaultModel,
		MaxResults:   maxResults,
	}}
}

func TestImageGenerateToolUsesProviderResultLimit(t *testing.T) {
	provider := &fakeImageGenerationProvider{id: "test-provider", maxResults: 2}
	tool := NewImageGenerateTool(
		t.TempDir(),
		"custom-image-model",
		media.NewFileMediaStore(),
		WithImageGenerationProvider(provider),
	)

	result := tool.Execute(t.Context(), map[string]any{"prompt": "two icons", "count": float64(4)})
	if result.IsError {
		t.Fatalf("Execute returned error: %s", result.ContentForLLM())
	}
	if provider.request.Count != 2 {
		t.Fatalf("request count = %d, want provider limit 2", provider.request.Count)
	}
}

func (p *fakeImageGenerationProvider) GenerateImage(
	_ context.Context,
	req providers.ImageGenerationRequest,
) (*providers.ImageGenerationResponse, error) {
	p.request = req
	return &providers.ImageGenerationResponse{Images: []providers.GeneratedImage{{
		Data:     []byte("fake-image"),
		MimeType: "image/png",
		Ext:      "png",
	}}}, nil
}

func TestImageGenerateToolCanUseInjectedProvider(t *testing.T) {
	store := media.NewFileMediaStore()
	provider := &fakeImageGenerationProvider{
		id:           "test-provider",
		defaultModel: "test-default-image-model",
	}
	tool := NewImageGenerateTool(
		t.TempDir(),
		"custom-image-model",
		store,
		WithImageGenerationProvider(provider),
	)

	result := tool.Execute(
		toolshared.WithToolContext(t.Context(), "telegram", "chat-1"),
		map[string]any{"prompt": "make a tiny icon"},
	)
	if result.IsError {
		t.Fatalf("Execute returned error: %s", result.ContentForLLM())
	}
	if provider.request.Model != "custom-image-model" {
		t.Fatalf("model = %q, want custom-image-model", provider.request.Model)
	}
	if len(result.Media) != 1 {
		t.Fatalf("media refs = %d, want 1", len(result.Media))
	}
	if !result.Delivery.IsFinalHandled() {
		t.Fatal("expected default image generation result to be response-handled")
	}
}

func TestImageGenerateToolUsesConfiguredOutputDir(t *testing.T) {
	store := media.NewFileMediaStore()
	provider := &fakeImageGenerationProvider{id: "test-provider"}
	workspace := t.TempDir()
	tool := NewImageGenerateTool(
		workspace,
		"custom-image-model",
		store,
		WithImageGenerationProvider(provider),
		WithImageGenerationOutputDir("tmp/generated-images"),
	)

	result := tool.Execute(
		toolshared.WithToolContext(t.Context(), "telegram", "chat-1"),
		map[string]any{"prompt": "make a tiny icon"},
	)
	if result.IsError {
		t.Fatalf("Execute returned error: %s", result.ContentForLLM())
	}
	if result.Deliverable == nil || len(result.Deliverable.Artifacts) != 1 {
		t.Fatalf("artifacts = %#v, want 1", result.Deliverable)
	}
	wantPrefix := filepath.Join(workspace, "tmp", "generated-images") + string(filepath.Separator)
	if !strings.HasPrefix(result.Deliverable.Artifacts[0].LocalPath, wantPrefix) {
		t.Fatalf("artifact path = %q, want prefix %q", result.Deliverable.Artifacts[0].LocalPath, wantPrefix)
	}
}

func TestImageGenerateToolImmediateContinueLeavesResponseUnhandled(t *testing.T) {
	store := media.NewFileMediaStore()
	provider := &fakeImageGenerationProvider{
		id:           "test-provider",
		defaultModel: "test-default-image-model",
	}
	tool := NewImageGenerateTool(
		t.TempDir(),
		"custom-image-model",
		store,
		WithImageGenerationProvider(provider),
	)

	result := tool.Execute(
		toolshared.WithToolContext(t.Context(), "telegram", "chat-1"),
		map[string]any{
			"prompt":          "make the first architecture diagram",
			"delivery_intent": string(toolshared.DeliveryImmediateContinue),
		},
	)
	if result.IsError {
		t.Fatalf("Execute returned error: %s", result.ContentForLLM())
	}
	if result.Delivery.IsFinalHandled() {
		t.Fatal("expected immediate_continue image generation result to leave response unhandled")
	}
	if !result.Delivery.IsImmediate() {
		t.Fatal("expected immediate_continue image generation result to request immediate delivery")
	}
	if !result.Delivery.SuppressesImplicitUserOutput() {
		t.Fatal("expected immediate_continue image generation result to suppress implicit delivery")
	}
	if len(result.Media) != 1 {
		t.Fatalf("media refs = %d, want 1", len(result.Media))
	}
}
