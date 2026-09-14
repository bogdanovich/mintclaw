package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const (
	defaultImageGenerationSize = "1024x1024"
	defaultImageEditingSize    = "auto"
)

// ImageGenerateTool generates images through a provider adapter and returns
// generated files through the MediaStore outbound media pipeline.
type ImageGenerateTool struct {
	workspace     string
	model         string
	fallbacks     []string
	outputDir     string
	provider      providers.ImageGenerationProvider
	resolver      ImageGenerationProviderResolver
	resolveOnce   sync.Once
	candidates    []imageGenerationCandidate
	resolveErr    error
	mediaStore    media.MediaStore
	restrict      bool
	maxInputBytes int
	allowPaths    []*regexp.Regexp
}

type ImageGenerateToolOption func(*ImageGenerateTool)

type imageGenerationCandidate struct {
	provider     providers.ImageGenerationProvider
	model        string
	capabilities providers.ImageGenerationCapabilities
}

type imageGenerationAttempt struct {
	provider string
	model    string
	reason   providers.FailoverReason
}

// ImageGenerationProviderResolver resolves the configured image-model selector
// to one provider instance and its native model identifier.
type ImageGenerationProviderResolver func(
	model string,
) (providers.ImageGenerationProvider, string, error)

func WithImageGenerationProvider(provider providers.ImageGenerationProvider) ImageGenerateToolOption {
	return func(t *ImageGenerateTool) {
		if provider != nil {
			t.provider = provider
		}
	}
}

// WithImageGenerationProviderResolver supplies config-aware provider
// resolution while retaining legacy GPT Image resolution as the default.
func WithImageGenerationProviderResolver(resolver ImageGenerationProviderResolver) ImageGenerateToolOption {
	return func(t *ImageGenerateTool) {
		if resolver != nil {
			t.resolver = resolver
		}
	}
}

// WithImageGenerationFallbacks configures ordered image-model selectors that
// are attempted only after a typed, recoverable provider failure.
func WithImageGenerationFallbacks(fallbacks []string) ImageGenerateToolOption {
	return func(t *ImageGenerateTool) {
		t.fallbacks = append([]string(nil), fallbacks...)
	}
}

func WithImageGenerationOutputDir(outputDir string) ImageGenerateToolOption {
	return func(t *ImageGenerateTool) {
		t.outputDir = strings.TrimSpace(outputDir)
	}
}

func NewImageGenerateTool(
	workspace string,
	model string,
	store media.MediaStore,
	options ...ImageGenerateToolOption,
) *ImageGenerateTool {
	tool := &ImageGenerateTool{
		workspace:     workspace,
		model:         model,
		mediaStore:    store,
		restrict:      true,
		maxInputBytes: defaultImageEditMaxInputBytes,
		resolver:      providers.CreateImageGenerationProviderFromModel,
	}
	for _, option := range options {
		option(tool)
	}
	return tool
}

func (t *ImageGenerateTool) SetMediaStore(store media.MediaStore) {
	t.mediaStore = store
}

func (t *ImageGenerateTool) Name() string { return "image_generate" }

func (t *ImageGenerateTool) Description() string {
	return `Generate or edit an image and send it to the current chat.

Use this when the user asks to create an image, infographic, diagram, poster, visual summary, or other generated raster artwork. The active image backend is selected by the configured image model alias or legacy GPT Image selector.

For requests to modify, caption, translate, restyle, or make a meme from an existing image, set action="edit" and pass the real source path or media:// reference in input_images. Paths are exposed by current-turn [image:/path] tags. Never claim to preserve a reference image while using prompt-only generation. For source-preserving edits, use input_fidelity="high".

When generating multiple distinct images for one user request, call this tool once per image with count=1. Set delivery_intent="immediate_continue" on every non-final image so the assistant continues after delivering it, and use delivery_intent="final_handled" or omit delivery_intent on the final image.`
}

func (t *ImageGenerateTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"generate", "edit"},
				"description": "Use edit when modifying an existing image; otherwise generate. If omitted, input_images selects edit mode.",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "Image generation or editing instructions.",
			},
			"input_images": map[string]any{
				"type":        "array",
				"description": "Source image paths from current [image:/path] tags or trusted media:// references. Required for edit mode.",
				"items": map[string]any{
					"type": "string",
				},
				"minItems": 1,
				"maxItems": maxImageEditInputs,
			},
			"input_fidelity": map[string]any{
				"type":        "string",
				"enum":        []string{"low", "high"},
				"description": "Edit-only fidelity to the source image. Defaults to high.",
			},
			"size": map[string]any{
				"type":        "string",
				"description": "Output size. Defaults to 1024x1024 for generation and auto for editing. Supported examples: 1024x1024, 1536x1024, 1024x1536, 2048x2048, 3840x2160.",
			},
			"quality": map[string]any{
				"type":        "string",
				"enum":        []string{"low", "medium", "high", "auto"},
				"description": "Optional quality hint.",
			},
			"output_format": map[string]any{
				"type":        "string",
				"enum":        []string{"png", "jpeg", "webp"},
				"description": "Output image format. Defaults to png.",
			},
			"count": map[string]any{
				"type":        "integer",
				"description": "Number of images to generate. Defaults to 1 and is capped by the active provider.",
			},
			"delivery_intent": map[string]any{
				"type": "string",
				"enum": []string{
					string(toolshared.DeliveryImmediateContinue),
					string(toolshared.DeliveryFinalHandled),
				},
				"description": "Delivery policy for this generated image. Use immediate_continue for non-final images in a multi-image task. Use final_handled, or omit, when this image satisfies the user request.",
			},
		},
		"required": []string{"prompt"},
	}
}

func (t *ImageGenerateTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	prompt, _ := args["prompt"].(string)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return toolshared.ErrorResult("prompt is required")
	}
	if t.mediaStore == nil {
		return toolshared.ErrorResult("media store not configured")
	}
	candidates, err := t.resolveImageGenerationCandidates()
	if err != nil {
		return toolshared.ErrorResult(fmt.Sprintf("image generation provider not configured: %v", err)).
			WithError(err)
	}

	action, inputLocations, err := readImageActionAndInputs(args)
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	editing := action == imageActionEdit
	var inputImages []providers.ImageGenerationInput
	if editing {
		maxInputImages, maxInputBytes := imageGenerationInputBounds(candidates)
		for index, candidate := range candidates {
			if !candidate.capabilities.Editing {
				return toolshared.ErrorResult(fmt.Sprintf(
					"configured image provider %d does not support editing",
					index+1,
				))
			}
		}
		inputImages, err = t.resolveImageInputs(
			inputLocations,
			maxInputImages,
			maxInputBytes,
		)
		if err != nil {
			return toolshared.ErrorResult(err.Error())
		}
	}
	defaultSize := defaultImageGenerationSize
	inputFidelity := ""
	if editing {
		defaultSize = defaultImageEditingSize
		inputFidelity = readStringDefault(args, "input_fidelity", "high")
	}
	baseRequest := providers.ImageGenerationRequest{
		Prompt:        prompt,
		Size:          readStringDefault(args, "size", defaultSize),
		Quality:       readStringDefault(args, "quality", ""),
		OutputFormat:  readStringDefault(args, "output_format", "png"),
		InputImages:   inputImages,
		InputFidelity: inputFidelity,
	}
	var (
		resp       *providers.ImageGenerationResponse
		successful imageGenerationCandidate
		request    providers.ImageGenerationRequest
		attempts   []imageGenerationAttempt
	)
	for index, candidate := range candidates {
		request = baseRequest
		request.Model = candidate.model
		if strings.TrimSpace(request.Model) == "" {
			request.Model = candidate.capabilities.DefaultModel
		}
		request.Count = readImageCount(args["count"], candidate.capabilities.MaxResults)
		resp, err = candidate.provider.GenerateImage(ctx, request)
		if err == nil {
			successful = candidate
			break
		}

		attempt, eligible := classifyImageGenerationAttempt(err, candidate)
		attempts = append(attempts, attempt)
		if resp != nil || index == len(candidates)-1 || !eligible || ctx.Err() != nil {
			return imageGenerationFailureResult(attempts)
		}
	}
	if resp == nil {
		return toolshared.ErrorResult("image generation returned no response")
	}
	images := resp.Images
	if len(images) == 0 {
		return toolshared.ErrorResult("image generation returned no images")
	}

	refs := make([]string, 0, len(images))
	paths := make([]string, 0, len(images))
	scope := t.mediaScope(ctx)
	for i, image := range images {
		path, err := t.writeGeneratedImage(image, i)
		if err != nil {
			return toolshared.ErrorResult(fmt.Sprintf("failed to write generated image: %v", err)).WithError(err)
		}
		ref, err := t.mediaStore.Store(path, media.MediaMeta{
			Filename:      filepath.Base(path),
			ContentType:   image.MimeType,
			Source:        "tool:image_generate",
			CleanupPolicy: media.CleanupPolicyDeleteOnCleanup,
		}, scope)
		if err != nil {
			return toolshared.ErrorResult(fmt.Sprintf("failed to register generated image: %v", err)).WithError(err)
		}
		refs = append(refs, ref)
		paths = append(paths, path)
	}

	verb := "Generated"
	if editing {
		verb = "Edited"
	}
	message := fmt.Sprintf(
		"%s %d image(s) with %s via %s.",
		verb,
		len(refs),
		request.Model,
		successful.capabilities.ProviderID,
	)
	result := toolshared.MediaResult(message, refs)
	switch readDeliveryIntentDefault(args, toolshared.DeliveryFinalHandled) {
	case toolshared.DeliveryImmediateContinue:
		result.WithDeliveryIntent(toolshared.DeliveryImmediateContinue)
	default:
		result.WithDeliveryIntent(toolshared.DeliveryFinalHandled)
	}
	for index, path := range paths {
		if index >= len(result.Deliverable.Artifacts) {
			result.Deliverable.Artifacts = append(
				result.Deliverable.Artifacts,
				taskresult.Artifact{Ref: "file:" + path},
			)
		}
		result.Deliverable.Artifacts[index].LocalPath = path
	}
	return result
}

func (t *ImageGenerateTool) resolveImageGenerationCandidates() ([]imageGenerationCandidate, error) {
	t.resolveOnce.Do(func() {
		if t.provider != nil {
			capabilities := providers.ImageCapabilities(t.provider)
			if !capabilities.Supported {
				t.resolveErr = fmt.Errorf("provider does not declare image generation support")
				return
			}
			t.candidates = []imageGenerationCandidate{{
				provider: t.provider, model: t.model, capabilities: capabilities,
			}}
			return
		}

		selectors := append([]string{t.model}, t.fallbacks...)
		t.candidates = make([]imageGenerationCandidate, 0, len(selectors))
		for index, selector := range selectors {
			provider, model, err := t.resolver(selector)
			if err != nil {
				t.resolveErr = fmt.Errorf("resolve image provider %d: %w", index+1, err)
				return
			}
			if provider == nil {
				t.resolveErr = fmt.Errorf("resolve image provider %d: provider is nil", index+1)
				return
			}
			capabilities := providers.ImageCapabilities(provider)
			if !capabilities.Supported {
				t.resolveErr = fmt.Errorf("image provider %d does not declare image generation support", index+1)
				return
			}
			t.candidates = append(t.candidates, imageGenerationCandidate{
				provider: provider, model: model, capabilities: capabilities,
			})
		}
	})
	return t.candidates, t.resolveErr
}

func imageGenerationInputBounds(candidates []imageGenerationCandidate) (int, int) {
	maxImages := maxImageEditInputs
	maxBytes := 0
	for _, candidate := range candidates {
		capabilities := candidate.capabilities
		if capabilities.MaxInputImages > 0 && capabilities.MaxInputImages < maxImages {
			maxImages = capabilities.MaxInputImages
		}
		if capabilities.MaxInputBytes > 0 && (maxBytes == 0 || capabilities.MaxInputBytes < maxBytes) {
			maxBytes = capabilities.MaxInputBytes
		}
	}
	return maxImages, maxBytes
}

func classifyImageGenerationAttempt(
	err error,
	candidate imageGenerationCandidate,
) (imageGenerationAttempt, bool) {
	providerID := candidate.capabilities.ProviderID
	classified := providers.ClassifyError(err, providerID, candidate.model)
	attempt := imageGenerationAttempt{
		provider: providerID,
		model:    candidate.model,
		reason:   providers.FailoverUnknown,
	}
	if classified == nil {
		return attempt, false
	}
	attempt.reason = classified.Reason
	switch classified.Reason {
	case providers.FailoverBilling,
		providers.FailoverRateLimit,
		providers.FailoverNetwork,
		providers.FailoverTimeout:
		return attempt, true
	default:
		return attempt, false
	}
}

func imageGenerationFailureResult(attempts []imageGenerationAttempt) *toolshared.ToolResult {
	parts := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		parts = append(parts, fmt.Sprintf(
			"%s/%s (%s)",
			boundedImageMetadata(attempt.provider),
			boundedImageMetadata(attempt.model),
			attempt.reason,
		))
	}
	message := fmt.Sprintf(
		"image generation failed after %d provider attempt(s): %s",
		len(parts),
		strings.Join(parts, ", "),
	)
	err := fmt.Errorf("%s", message)
	return toolshared.ErrorResult(message).WithError(err)
}

func boundedImageMetadata(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "unknown"
	}
	runes := []rune(value)
	if len(runes) > 80 {
		return string(runes[:80]) + "..."
	}
	return value
}

func (t *ImageGenerateTool) writeGeneratedImage(image providers.GeneratedImage, index int) (string, error) {
	dir := t.effectiveOutputDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("image-%d-%s.%s", index+1, uuid.NewString(), image.Ext)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, image.Data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (t *ImageGenerateTool) effectiveOutputDir() string {
	if dir := strings.TrimSpace(t.outputDir); dir != "" {
		dir = os.ExpandEnv(dir)
		if strings.HasPrefix(dir, "~/") || dir == "~" {
			if home, err := os.UserHomeDir(); err == nil && home != "" {
				if dir == "~" {
					return home
				}
				return filepath.Join(home, strings.TrimPrefix(dir, "~/"))
			}
		}
		if filepath.IsAbs(dir) {
			return filepath.Clean(dir)
		}
		if t.workspace != "" {
			return filepath.Join(t.workspace, dir)
		}
		return filepath.Clean(dir)
	}
	return filepath.Join(media.TempDir(), "image_generate")
}

func (t *ImageGenerateTool) mediaScope(ctx context.Context) string {
	parts := []string{"tool:image_generate"}
	if channel := toolshared.ToolChannel(ctx); channel != "" {
		parts = append(parts, channel)
	}
	if chatID := toolshared.ToolChatID(ctx); chatID != "" {
		parts = append(parts, chatID)
	}
	if sessionKey := toolshared.ToolSessionKey(ctx); sessionKey != "" {
		parts = append(parts, sessionKey)
	}
	return strings.Join(parts, ":")
}

func readStringDefault(args map[string]any, key string, fallback string) string {
	value, _ := args[key].(string)
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func readImageCount(raw any, maxResults int) int {
	count := 1
	switch v := raw.(type) {
	case int:
		count = v
	case float64:
		count = int(v)
	case json.Number:
		if parsed, err := v.Int64(); err == nil {
			count = int(parsed)
		}
	}
	if count < 1 {
		return 1
	}
	if maxResults > 0 && count > maxResults {
		return maxResults
	}
	return count
}

func readDeliveryIntentDefault(args map[string]any, fallback toolshared.DeliveryIntent) toolshared.DeliveryIntent {
	raw, ok := args["delivery_intent"]
	if !ok {
		return fallback
	}
	value, _ := raw.(string)
	switch toolshared.DeliveryIntent(strings.TrimSpace(value)) {
	case toolshared.DeliveryImmediateContinue:
		return toolshared.DeliveryImmediateContinue
	case toolshared.DeliveryFinalHandled:
		return toolshared.DeliveryFinalHandled
	default:
		return fallback
	}
}
