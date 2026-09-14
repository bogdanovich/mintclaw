package tools

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type fakeImageGenerationProvider struct {
	id             string
	defaultModel   string
	maxResults     int
	request        providers.ImageGenerationRequest
	calls          int
	editing        bool
	maxInputImages int
	maxInputBytes  int
	err            error
	response       *providers.ImageGenerationResponse
	returnNil      bool
	unsupported    bool
}

func (p *fakeImageGenerationProvider) Capabilities() providers.ProviderCapabilities {
	if p.unsupported {
		return providers.ProviderCapabilities{}
	}
	maxResults := p.maxResults
	if maxResults == 0 {
		maxResults = 4
	}
	maxInputImages := p.maxInputImages
	if maxInputImages == 0 {
		maxInputImages = maxImageEditInputs
	}
	return providers.ProviderCapabilities{ImageGeneration: providers.ImageGenerationCapabilities{
		Supported:      true,
		Editing:        p.editing,
		ProviderID:     p.id,
		DefaultModel:   p.defaultModel,
		MaxResults:     maxResults,
		MaxInputImages: maxInputImages,
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
	if p.err != nil {
		return p.response, p.err
	}
	if p.returnNil {
		return nil, nil
	}
	if p.response != nil {
		return p.response, nil
	}
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

func TestImageGenerateToolResolvesConfiguredProviderWithoutChangingToolContract(t *testing.T) {
	store := media.NewFileMediaStore()
	provider := &fakeImageGenerationProvider{id: "gemini", editing: true}
	resolverCalls := 0
	tool := NewImageGenerateTool(
		t.TempDir(),
		"nano-banana",
		store,
		WithImageGenerationProviderResolver(func(
			selector string,
		) (providers.ImageGenerationProvider, string, error) {
			resolverCalls++
			if selector != "nano-banana" {
				t.Fatalf("selector = %q", selector)
			}
			return provider, "gemini-3.1-flash-image", nil
		}),
	)

	result := tool.Execute(
		toolshared.WithToolContext(t.Context(), "telegram", "chat-1"),
		map[string]any{"prompt": "make a tiny icon"},
	)
	if result.IsError {
		t.Fatalf("Execute returned error: %s", result.ContentForLLM())
	}
	if resolverCalls != 1 || provider.request.Model != "gemini-3.1-flash-image" {
		t.Fatalf("resolver calls/model = %d/%q", resolverCalls, provider.request.Model)
	}
	if tool.Name() != "image_generate" || len(result.Media) != 1 {
		t.Fatalf("tool/result contract changed: name=%q media=%d", tool.Name(), len(result.Media))
	}
}

func TestImageGenerateToolUsesPrimaryWithoutCallingFallback(t *testing.T) {
	primary := &fakeImageGenerationProvider{id: "openai-codex"}
	fallback := &fakeImageGenerationProvider{id: "gemini"}
	tool := NewImageGenerateTool(
		t.TempDir(),
		"gpt-image",
		media.NewFileMediaStore(),
		WithImageGenerationFallbacks([]string{"nano-banana"}),
		WithImageGenerationProviderResolver(imageProviderResolver(t, map[string]resolvedTestImageProvider{
			"gpt-image":   {provider: primary, model: "gpt-image-2"},
			"nano-banana": {provider: fallback, model: "gemini-3.1-flash-image"},
		})),
	)

	result := tool.Execute(t.Context(), map[string]any{"prompt": "mint robot"})
	if result.IsError {
		t.Fatalf("Execute returned error: %s", result.ContentForLLM())
	}
	if primary.calls != 1 || fallback.calls != 0 {
		t.Fatalf("provider calls = primary %d, fallback %d; want 1/0", primary.calls, fallback.calls)
	}
	if !strings.Contains(result.ContentForLLM(), "via openai-codex") {
		t.Fatalf("result = %q, want primary provider", result.ContentForLLM())
	}
}

func TestImageGenerateToolFallsBackForRecoverableEditFailure(t *testing.T) {
	workspace := t.TempDir()
	sourcePath := filepath.Join(workspace, "source.png")
	sourceBytes := encodeTinyPNG(t)
	if err := os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	primary := &fakeImageGenerationProvider{
		id:      "openai-codex",
		editing: true,
		err: &providererrors.ProviderError{
			Kind: providererrors.KindRateLimit, SafeMessage: "rate limited",
		},
	}
	fallback := &fakeImageGenerationProvider{id: "gemini", editing: true, maxResults: 1}
	tool := NewImageGenerateTool(
		workspace,
		"gpt-image",
		media.NewFileMediaStore(),
		WithImageGenerationFallbacks([]string{"nano-banana"}),
		WithImageGenerationProviderResolver(imageProviderResolver(t, map[string]resolvedTestImageProvider{
			"gpt-image":   {provider: primary, model: "gpt-image-2"},
			"nano-banana": {provider: fallback, model: "gemini-3.1-flash-image"},
		})),
	)

	result := tool.Execute(t.Context(), map[string]any{
		"action": "edit", "prompt": "add a mint circle", "input_images": []any{sourcePath}, "count": 3,
	})
	if result.IsError {
		t.Fatalf("Execute returned error: %s", result.ContentForLLM())
	}
	if primary.calls != 1 || fallback.calls != 1 {
		t.Fatalf("provider calls = primary %d, fallback %d; want 1/1", primary.calls, fallback.calls)
	}
	if fallback.request.Count != 1 || len(fallback.request.InputImages) != 1 ||
		!bytes.Equal(fallback.request.InputImages[0].Data, sourceBytes) {
		t.Fatalf("fallback request = %#v, want bounded edit with exact source bytes", fallback.request)
	}
	if !strings.Contains(result.ContentForLLM(), "via gemini") {
		t.Fatalf("result = %q, want fallback provider", result.ContentForLLM())
	}
}

func TestImageGenerateToolDoesNotApplyFallbackEditLimitsBeforePrimarySucceeds(t *testing.T) {
	workspace := t.TempDir()
	sourcePath := filepath.Join(workspace, "source.png")
	if err := os.WriteFile(sourcePath, encodeTinyPNG(t), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	for _, test := range []struct {
		name     string
		fallback *fakeImageGenerationProvider
	}{
		{name: "editing unsupported", fallback: &fakeImageGenerationProvider{id: "gemini"}},
		{
			name: "input count",
			fallback: &fakeImageGenerationProvider{
				id: "gemini", editing: true, maxInputImages: 1,
			},
		},
		{
			name: "input bytes",
			fallback: &fakeImageGenerationProvider{
				id: "gemini", editing: true, maxInputBytes: 1,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			primary := &fakeImageGenerationProvider{id: "openai-codex", editing: true}
			tool := NewImageGenerateTool(
				workspace,
				"gpt-image",
				media.NewFileMediaStore(),
				WithImageGenerationFallbacks([]string{"nano-banana"}),
				WithImageGenerationProviderResolver(imageProviderResolver(t, map[string]resolvedTestImageProvider{
					"gpt-image":   {provider: primary, model: "gpt-image-2"},
					"nano-banana": {provider: test.fallback, model: "gemini-3.1-flash-image"},
				})),
			)

			result := tool.Execute(t.Context(), map[string]any{
				"action": "edit", "prompt": "edit both", "input_images": []any{sourcePath, sourcePath},
			})
			if result.IsError {
				t.Fatalf("Execute returned error: %s", result.ContentForLLM())
			}
			if primary.calls != 1 || test.fallback.calls != 0 {
				t.Fatalf(
					"provider calls = primary %d, fallback %d; want 1/0",
					primary.calls,
					test.fallback.calls,
				)
			}
			if len(primary.request.InputImages) != 2 {
				t.Fatalf("primary input images = %d, want 2", len(primary.request.InputImages))
			}
		})
	}
}

func TestImageGenerateToolValidatesFallbackEditLimitsWhenReached(t *testing.T) {
	workspace := t.TempDir()
	sourcePath := filepath.Join(workspace, "source.png")
	if err := os.WriteFile(sourcePath, encodeTinyPNG(t), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	for _, test := range []struct {
		name     string
		fallback *fakeImageGenerationProvider
		want     string
	}{
		{
			name: "editing unsupported",
			fallback: &fakeImageGenerationProvider{
				id: "gemini",
			},
			want: "editing is not supported",
		},
		{
			name: "input count",
			fallback: &fakeImageGenerationProvider{
				id: "gemini", editing: true, maxInputImages: 1,
			},
			want: "maximum 1",
		},
		{
			name: "input bytes",
			fallback: &fakeImageGenerationProvider{
				id: "gemini", editing: true, maxInputBytes: 1,
			},
			want: "byte limit",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			primary := &fakeImageGenerationProvider{
				id: "openai-codex", editing: true,
				err: &providererrors.ProviderError{
					Kind: providererrors.KindRateLimit, SafeMessage: "rate limited",
				},
			}
			tool := NewImageGenerateTool(
				workspace,
				"gpt-image",
				media.NewFileMediaStore(),
				WithImageGenerationFallbacks([]string{"nano-banana"}),
				WithImageGenerationProviderResolver(imageProviderResolver(t, map[string]resolvedTestImageProvider{
					"gpt-image":   {provider: primary, model: "gpt-image-2"},
					"nano-banana": {provider: test.fallback, model: "gemini-3.1-flash-image"},
				})),
			)

			result := tool.Execute(t.Context(), map[string]any{
				"action": "edit", "prompt": "edit both", "input_images": []any{sourcePath, sourcePath},
			})
			if !result.IsError || !strings.Contains(result.ContentForLLM(), test.want) ||
				!strings.Contains(result.ContentForLLM(), "provider 2") {
				t.Fatalf("Execute result = %q, want provider 2 error containing %q", result.ContentForLLM(), test.want)
			}
			if primary.calls != 1 || test.fallback.calls != 0 {
				t.Fatalf(
					"provider calls = primary %d, fallback %d; want 1/0",
					primary.calls,
					test.fallback.calls,
				)
			}
		})
	}
}

func TestImageGenerateToolDoesNotFallbackForNonRecoverableFailure(t *testing.T) {
	for _, test := range []struct {
		name string
		kind providererrors.Kind
	}{
		{name: "authentication", kind: providererrors.KindAuthentication},
		{name: "invalid request", kind: providererrors.KindInvalidRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			primary := &fakeImageGenerationProvider{
				id:  "openai-codex",
				err: &providererrors.ProviderError{Kind: test.kind, SafeMessage: "do not retry"},
			}
			fallback := &fakeImageGenerationProvider{id: "gemini"}
			tool := NewImageGenerateTool(
				t.TempDir(),
				"gpt-image",
				media.NewFileMediaStore(),
				WithImageGenerationFallbacks([]string{"nano-banana"}),
				WithImageGenerationProviderResolver(imageProviderResolver(t, map[string]resolvedTestImageProvider{
					"gpt-image":   {provider: primary, model: "gpt-image-2"},
					"nano-banana": {provider: fallback, model: "gemini-3.1-flash-image"},
				})),
			)

			result := tool.Execute(t.Context(), map[string]any{"prompt": "mint robot"})
			if !result.IsError {
				t.Fatal("Execute succeeded, want provider error")
			}
			if primary.calls != 1 || fallback.calls != 0 {
				t.Fatalf("provider calls = primary %d, fallback %d; want 1/0", primary.calls, fallback.calls)
			}
		})
	}
}

func TestImageGenerationFallbackEligibilityUsesTypedProviderFailures(t *testing.T) {
	candidate := imageGenerationCandidate{
		model: "gpt-image-2",
		capabilities: providers.ImageGenerationCapabilities{
			ProviderID: "openai-codex",
		},
	}
	for _, test := range []struct {
		kind providererrors.Kind
		want bool
	}{
		{kind: providererrors.KindBilling, want: true},
		{kind: providererrors.KindRateLimit, want: true},
		{kind: providererrors.KindNetwork, want: true},
		{kind: providererrors.KindTimeout, want: true},
		{kind: providererrors.KindTransient, want: true},
		{kind: providererrors.KindAuthentication, want: false},
		{kind: providererrors.KindInvalidRequest, want: false},
		{kind: providererrors.KindCanceled, want: false},
		{kind: providererrors.KindUnknown, want: false},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			_, got := classifyImageGenerationAttempt(&providererrors.ProviderError{Kind: test.kind}, candidate)
			if got != test.want {
				t.Fatalf("fallback eligibility = %t, want %t", got, test.want)
			}
		})
	}
}

func TestImageGenerateToolReportsBoundedSecretSafeFallbackFailure(t *testing.T) {
	primary := &fakeImageGenerationProvider{
		id: "openai-codex",
		err: &providererrors.ProviderError{
			Kind: providererrors.KindRateLimit, SafeMessage: "secret-primary-value",
		},
	}
	fallback := &fakeImageGenerationProvider{
		id: "gemini",
		err: &providererrors.ProviderError{
			Kind: providererrors.KindTimeout, SafeMessage: "secret-fallback-value",
		},
	}
	tool := NewImageGenerateTool(
		t.TempDir(),
		"gpt-image",
		media.NewFileMediaStore(),
		WithImageGenerationFallbacks([]string{"nano-banana"}),
		WithImageGenerationProviderResolver(imageProviderResolver(t, map[string]resolvedTestImageProvider{
			"gpt-image":   {provider: primary, model: "gpt-image-2"},
			"nano-banana": {provider: fallback, model: "gemini-3.1-flash-image"},
		})),
	)

	result := tool.Execute(t.Context(), map[string]any{"prompt": "mint robot"})
	content := result.ContentForLLM()
	if !result.IsError || !strings.Contains(content, "after 2 provider attempt(s)") {
		t.Fatalf("Execute result = %q, want bounded aggregate failure", content)
	}
	if strings.Contains(content, "secret-primary-value") || strings.Contains(content, "secret-fallback-value") {
		t.Fatalf("Execute result leaked provider details: %q", content)
	}
	if primary.calls != 1 || fallback.calls != 1 {
		t.Fatalf("provider calls = primary %d, fallback %d; want 1/1", primary.calls, fallback.calls)
	}
}

func TestImageGenerateToolDoesNotFallbackAfterMalformedSuccess(t *testing.T) {
	primary := &fakeImageGenerationProvider{
		id:       "openai-codex",
		response: &providers.ImageGenerationResponse{},
	}
	fallback := &fakeImageGenerationProvider{id: "gemini"}
	tool := NewImageGenerateTool(
		t.TempDir(),
		"gpt-image",
		media.NewFileMediaStore(),
		WithImageGenerationFallbacks([]string{"nano-banana"}),
		WithImageGenerationProviderResolver(imageProviderResolver(t, map[string]resolvedTestImageProvider{
			"gpt-image":   {provider: primary, model: "gpt-image-2"},
			"nano-banana": {provider: fallback, model: "gemini-3.1-flash-image"},
		})),
	)

	result := tool.Execute(t.Context(), map[string]any{"prompt": "mint robot"})
	if !result.IsError || !strings.Contains(result.ContentForLLM(), "returned no images") {
		t.Fatalf("Execute result = %q, want malformed success error", result.ContentForLLM())
	}
	if primary.calls != 1 || fallback.calls != 0 {
		t.Fatalf("provider calls = primary %d, fallback %d; want 1/0", primary.calls, fallback.calls)
	}
}

func TestImageGenerateToolDoesNotFallbackAfterProviderReturnedResultWithError(t *testing.T) {
	primary := &fakeImageGenerationProvider{
		id: "openai-codex",
		err: &providererrors.ProviderError{
			Kind: providererrors.KindRateLimit, SafeMessage: "rate limited after result",
		},
		response: &providers.ImageGenerationResponse{Images: []providers.GeneratedImage{{
			Data: []byte("partial-result"), MimeType: "image/png", Ext: "png",
		}}},
	}
	fallback := &fakeImageGenerationProvider{id: "gemini"}
	tool := NewImageGenerateTool(
		t.TempDir(),
		"gpt-image",
		media.NewFileMediaStore(),
		WithImageGenerationFallbacks([]string{"nano-banana"}),
		WithImageGenerationProviderResolver(imageProviderResolver(t, map[string]resolvedTestImageProvider{
			"gpt-image":   {provider: primary, model: "gpt-image-2"},
			"nano-banana": {provider: fallback, model: "gemini-3.1-flash-image"},
		})),
	)

	result := tool.Execute(t.Context(), map[string]any{"prompt": "mint robot"})
	if !result.IsError {
		t.Fatal("Execute succeeded, want provider error")
	}
	if primary.calls != 1 || fallback.calls != 0 {
		t.Fatalf("provider calls = primary %d, fallback %d; want 1/0", primary.calls, fallback.calls)
	}
	if len(result.Media) != 0 {
		t.Fatalf("error result media = %#v, want no partial delivery", result.Media)
	}
}

func TestImageGenerateToolResolvesEntireFallbackChainBeforeCallingProvider(t *testing.T) {
	primary := &fakeImageGenerationProvider{id: "openai-codex"}
	tool := NewImageGenerateTool(
		t.TempDir(),
		"gpt-image",
		media.NewFileMediaStore(),
		WithImageGenerationFallbacks([]string{"missing"}),
		WithImageGenerationProviderResolver(func(selector string) (providers.ImageGenerationProvider, string, error) {
			if selector == "gpt-image" {
				return primary, "gpt-image-2", nil
			}
			return nil, "", errors.New("fallback is unavailable")
		}),
	)

	result := tool.Execute(t.Context(), map[string]any{"prompt": "mint robot"})
	if !result.IsError || !strings.Contains(result.ContentForLLM(), "resolve image provider 2") {
		t.Fatalf("Execute result = %q, want eager fallback resolution error", result.ContentForLLM())
	}
	if primary.calls != 0 {
		t.Fatalf("primary calls = %d, want 0 before full chain resolves", primary.calls)
	}
}

func TestImageGenerateToolRejectsIncompatibleFallbackBeforeCallingPrimary(t *testing.T) {
	primary := &fakeImageGenerationProvider{id: "openai-codex"}
	fallback := &fakeImageGenerationProvider{id: "gemini", unsupported: true}
	tool := NewImageGenerateTool(
		t.TempDir(),
		"gpt-image",
		media.NewFileMediaStore(),
		WithImageGenerationFallbacks([]string{"not-an-image-model"}),
		WithImageGenerationProviderResolver(imageProviderResolver(t, map[string]resolvedTestImageProvider{
			"gpt-image":          {provider: primary, model: "gpt-image-2"},
			"not-an-image-model": {provider: fallback, model: "gemini-3.1-flash"},
		})),
	)

	result := tool.Execute(t.Context(), map[string]any{"prompt": "mint robot"})
	if !result.IsError || !strings.Contains(result.ContentForLLM(), "does not declare image generation support") {
		t.Fatalf("Execute result = %q, want incompatible fallback error", result.ContentForLLM())
	}
	if primary.calls != 0 || fallback.calls != 0 {
		t.Fatalf("provider calls = primary %d, fallback %d; want 0/0", primary.calls, fallback.calls)
	}
}

type resolvedTestImageProvider struct {
	provider providers.ImageGenerationProvider
	model    string
}

func imageProviderResolver(
	t *testing.T,
	configured map[string]resolvedTestImageProvider,
) ImageGenerationProviderResolver {
	t.Helper()
	return func(selector string) (providers.ImageGenerationProvider, string, error) {
		resolved, ok := configured[selector]
		if !ok {
			t.Fatalf("unexpected image model selector %q", selector)
		}
		return resolved.provider, resolved.model, nil
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
