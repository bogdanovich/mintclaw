package tools

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type fakeImageGenerationProvider struct {
	id            string
	defaultModel  string
	maxResults    int
	request       providers.ImageGenerationRequest
	calls         int
	editing       bool
	maxInputBytes int
}

func (p *fakeImageGenerationProvider) Capabilities() providers.ProviderCapabilities {
	maxResults := p.maxResults
	if maxResults == 0 {
		maxResults = 4
	}
	return providers.ProviderCapabilities{ImageGeneration: providers.ImageGenerationCapabilities{
		Supported:      true,
		Editing:        p.editing,
		ProviderID:     p.id,
		DefaultModel:   p.defaultModel,
		MaxResults:     maxResults,
		MaxInputImages: maxImageEditInputs,
		MaxInputBytes:  p.maxInputBytes,
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
	p.calls++
	p.request = req
	return &providers.ImageGenerationResponse{Images: []providers.GeneratedImage{{
		Data:     []byte("fake-image"),
		MimeType: "image/png",
		Ext:      "png",
	}}}, nil
}

func TestImageGenerateToolEditsWithExactSourceBytes(t *testing.T) {
	workspace := t.TempDir()
	sourcePath := filepath.Join(workspace, "source.png")
	sourceBytes := encodeTinyPNG(t)
	if err := os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	provider := &fakeImageGenerationProvider{id: "test-provider", editing: true}
	tool := NewImageGenerateTool(
		workspace,
		"custom-image-model",
		media.NewFileMediaStore(),
		WithImageGenerationProvider(provider),
	)

	result := tool.Execute(t.Context(), map[string]any{
		"action":       "edit",
		"prompt":       "replace the caption with English",
		"input_images": []any{sourcePath},
	})
	if result.IsError {
		t.Fatalf("Execute returned error: %s", result.ContentForLLM())
	}
	if provider.calls != 1 || len(provider.request.InputImages) != 1 {
		t.Fatalf("provider calls/request = %d/%#v, want one edit input", provider.calls, provider.request)
	}
	input := provider.request.InputImages[0]
	if !bytes.Equal(input.Data, sourceBytes) || input.ContentType != "image/png" || input.Filename != "source.png" {
		t.Fatalf("input = %#v, want exact PNG source", input)
	}
	if provider.request.Size != "auto" || provider.request.InputFidelity != "high" {
		t.Fatalf("edit defaults = size %q fidelity %q, want auto/high", provider.request.Size,
			provider.request.InputFidelity)
	}
	if !strings.Contains(result.ContentForLLM(), "Edited 1 image") {
		t.Fatalf("result = %q, want edit outcome", result.ContentForLLM())
	}
}

func TestImageGenerateToolEditsTrustedMediaReference(t *testing.T) {
	workspace := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "outside.webp")
	sourceBytes := []byte("RIFF\x04\x00\x00\x00WEBPVP8 ")
	if err := os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store := media.NewFileMediaStore()
	ref, err := store.Store(sourcePath, media.MediaMeta{
		Filename:      "trusted.webp",
		ContentType:   "image/webp",
		CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "test")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	provider := &fakeImageGenerationProvider{id: "test-provider", editing: true}
	tool := NewImageGenerateTool(workspace, "model", store, WithImageGenerationProvider(provider))

	result := tool.Execute(t.Context(), map[string]any{
		"prompt":       "edit it",
		"input_images": []string{ref},
	})
	if result.IsError {
		t.Fatalf("Execute returned error: %s", result.ContentForLLM())
	}
	if len(provider.request.InputImages) != 1 || provider.request.InputImages[0].Filename != "trusted.webp" {
		t.Fatalf("provider input = %#v, want trusted media reference", provider.request.InputImages)
	}
}

func TestImageGenerateToolRejectsUnsafeEditInputsBeforeProvider(t *testing.T) {
	workspace := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "private.png")
	if err := os.WriteFile(outsidePath, encodeTinyPNG(t), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	textPath := filepath.Join(workspace, "not-image.txt")
	if err := os.WriteFile(textPath, []byte("not an image"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	provider := &fakeImageGenerationProvider{id: "test-provider", editing: true}
	tool := NewImageGenerateTool(
		workspace,
		"model",
		media.NewFileMediaStore(),
		WithImageGenerationProvider(provider),
		WithImageGenerationInputPolicy(true, 8, nil),
	)

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "missing", args: map[string]any{"action": "edit", "prompt": "edit"}, want: "requires"},
		{
			name: "outside workspace",
			args: map[string]any{"action": "edit", "prompt": "edit", "input_images": []any{outsidePath}},
			want: "not accessible",
		},
		{
			name: "oversized",
			args: map[string]any{"action": "edit", "prompt": "edit", "input_images": []any{textPath}},
			want: "size limit",
		},
		{
			name: "too many",
			args: map[string]any{
				"action": "edit", "prompt": "edit",
				"input_images": []any{"a", "b", "c", "d", "e"},
			},
			want: "too many",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := tool.Execute(t.Context(), test.args)
			if !result.IsError || !strings.Contains(result.ContentForLLM(), test.want) {
				t.Fatalf("Execute result = %q, want error containing %q", result.ContentForLLM(), test.want)
			}
			if strings.Contains(result.ContentForLLM(), outsidePath) {
				t.Fatalf("error leaked source path: %q", result.ContentForLLM())
			}
		})
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0 for rejected inputs", provider.calls)
	}
}

func TestImageGenerateToolRejectsUnsupportedImageType(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "not-image.txt")
	if err := os.WriteFile(path, []byte("not an image"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	provider := &fakeImageGenerationProvider{id: "test-provider", editing: true}
	tool := NewImageGenerateTool(workspace, "model", media.NewFileMediaStore(),
		WithImageGenerationProvider(provider))

	result := tool.Execute(t.Context(), map[string]any{
		"action": "edit", "prompt": "edit", "input_images": []any{path},
	})
	if !result.IsError || !strings.Contains(result.ContentForLLM(), "not a supported") {
		t.Fatalf("Execute result = %q, want unsupported image error", result.ContentForLLM())
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.calls)
	}
}

func encodeTinyPNG(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&output, img); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	return output.Bytes()
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
