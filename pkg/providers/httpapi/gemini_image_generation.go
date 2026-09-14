package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/providers/httperrors"
)

const (
	geminiDefaultImageGenerationModel = "gemini-3.1-flash-image"
	geminiImageResponseMIME           = "image/jpeg"
	geminiMaxImageResults             = 1
	geminiMaxInputImages              = 4
	// Inline Interactions requests are limited to 20 MB. Fourteen MiB of raw
	// images leaves room for base64 expansion, the prompt, and JSON framing.
	geminiMaxInputBytes        = 14 * 1024 * 1024
	geminiMaxPromptBytes       = 256 * 1024
	geminiMaxRequestBytes      = 20 * 1000 * 1000
	geminiMaxOutputBytes       = 32 * 1024 * 1024
	geminiMaxResponseBodyBytes = 48 * 1024 * 1024
)

var errGeminiImageResponseTooLarge = errors.New("gemini image response exceeded size limit")

type geminiInteractionInput struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
}

type geminiImageResponseFormat struct {
	Type        string `json:"type"`
	MIMEType    string `json:"mime_type,omitempty"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
	ImageSize   string `json:"image_size,omitempty"`
}

type geminiImageInteractionRequest struct {
	Model          string                    `json:"model"`
	Input          []geminiInteractionInput  `json:"input"`
	ResponseFormat geminiImageResponseFormat `json:"response_format"`
	Store          bool                      `json:"store"`
}

type geminiImageInteractionResponse struct {
	Status string `json:"status"`
	Steps  []struct {
		Type    string `json:"type"`
		Content []struct {
			Type     string `json:"type"`
			Data     string `json:"data"`
			MIMEType string `json:"mime_type"`
		} `json:"content"`
	} `json:"steps"`
}

func (p *GeminiProvider) GenerateImage(
	ctx context.Context,
	req ImageGenerationRequest,
) (*ImageGenerationResponse, error) {
	if p.apiBase == "" {
		return nil, fmt.Errorf("gemini image generation API base is not configured")
	}
	if p.apiKey == "" {
		return nil, fmt.Errorf("gemini image generation API key is not configured")
	}
	requestBody, err := buildGeminiImageRequest(req)
	if err != nil {
		return nil, err
	}
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("marshal gemini image request: %w", err)
	}
	if len(jsonData) > geminiMaxRequestBytes {
		return nil, fmt.Errorf("gemini image request exceeded size limit")
	}

	requestURL := p.apiBase + "/interactions"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("create gemini image request: %w", err)
	}
	p.applyHeaders(httpRequest)

	response, err := p.httpClient.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("send gemini image request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, httperrors.HandleResponse(response, p.apiBase)
	}

	body, err := readGeminiImageResponse(response.Body)
	if err != nil {
		return nil, err
	}
	var interaction geminiImageInteractionResponse
	if decodeErr := json.Unmarshal(body, &interaction); decodeErr != nil {
		return nil, fmt.Errorf("decode gemini image response: %w", decodeErr)
	}
	if interaction.Status != "completed" {
		return nil, fmt.Errorf("gemini image generation did not complete")
	}
	images, err := decodeGeminiInteractionImages(interaction)
	if err != nil {
		return nil, err
	}
	return &ImageGenerationResponse{Images: images}, nil
}

func buildGeminiImageRequest(req ImageGenerationRequest) (geminiImageInteractionRequest, error) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return geminiImageInteractionRequest{}, fmt.Errorf("gemini image prompt is required")
	}
	if len(prompt) > geminiMaxPromptBytes {
		return geminiImageInteractionRequest{}, fmt.Errorf("gemini image prompt exceeded size limit")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = geminiDefaultImageGenerationModel
	}
	if !supportedGeminiImageModel(model) {
		return geminiImageInteractionRequest{}, fmt.Errorf(
			"gemini model %q does not support native image generation",
			model,
		)
	}
	if req.Count < 0 || req.Count > geminiMaxImageResults {
		return geminiImageInteractionRequest{}, fmt.Errorf("gemini image generation supports one result per request")
	}
	if len(req.InputImages) > geminiMaxInputImages {
		return geminiImageInteractionRequest{}, fmt.Errorf("gemini image edit exceeded input image limit")
	}

	inputs := make([]geminiInteractionInput, 0, len(req.InputImages)+1)
	inputs = append(inputs, geminiInteractionInput{Type: "text", Text: prompt})
	totalInputBytes := 0
	for index, input := range req.InputImages {
		mimeType := strings.ToLower(strings.TrimSpace(input.ContentType))
		if !supportedGeminiInputMIME(mimeType) || len(input.Data) == 0 {
			return geminiImageInteractionRequest{}, fmt.Errorf(
				"gemini image edit input %d must be a non-empty PNG, JPEG, or WebP image",
				index+1,
			)
		}
		detectedMIME, _, ok := geminiImageType(input.Data)
		if !ok || detectedMIME != mimeType {
			return geminiImageInteractionRequest{}, fmt.Errorf(
				"gemini image edit input %d content type did not match its bytes",
				index+1,
			)
		}
		totalInputBytes += len(input.Data)
		if totalInputBytes > geminiMaxInputBytes {
			return geminiImageInteractionRequest{}, fmt.Errorf("gemini image edit exceeded input byte limit")
		}
		inputs = append(inputs, geminiInteractionInput{
			Type:     "image",
			Data:     base64.StdEncoding.EncodeToString(input.Data),
			MIMEType: mimeType,
		})
	}

	// Gemini's Interactions image response format currently accepts JPEG only.
	// Keep output_format portable at the tool boundary and normalize it here
	// rather than sending an unsupported MIME type to the provider.
	responseFormat := geminiImageResponseFormat{Type: "image", MIMEType: geminiImageResponseMIME}
	responseFormat.AspectRatio, responseFormat.ImageSize = geminiImageSize(req.Size)
	return geminiImageInteractionRequest{
		Model:          model,
		Input:          inputs,
		ResponseFormat: responseFormat,
		Store:          false,
	}, nil
}

func supportedGeminiImageModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if !strings.HasPrefix(model, "gemini-") {
		return false
	}
	return strings.Contains(model, "-image")
}

func supportedGeminiInputMIME(mimeType string) bool {
	switch mimeType {
	case "image/png", "image/jpeg", "image/webp":
		return true
	default:
		return false
	}
}

func geminiImageSize(size string) (aspectRatio string, imageSize string) {
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "1024x1024":
		return "1:1", "1K"
	case "1536x1024":
		return "3:2", "2K"
	case "1024x1536":
		return "2:3", "2K"
	case "2048x2048":
		return "1:1", "2K"
	case "3840x2160":
		return "16:9", "4K"
	default:
		return "", ""
	}
}

func readGeminiImageResponse(body io.Reader) ([]byte, error) {
	return readBoundedGeminiImageResponse(body, geminiMaxResponseBodyBytes)
}

func readBoundedGeminiImageResponse(body io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read gemini image response: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, errGeminiImageResponseTooLarge
	}
	return data, nil
}

func decodeGeminiInteractionImages(interaction geminiImageInteractionResponse) ([]GeneratedImage, error) {
	images := make([]GeneratedImage, 0, geminiMaxImageResults)
	for _, step := range interaction.Steps {
		if step.Type != "model_output" {
			continue
		}
		for _, content := range step.Content {
			if content.Type != "image" || strings.TrimSpace(content.Data) == "" {
				continue
			}
			image, err := decodeGeminiImage(content.Data, content.MIMEType, geminiMaxOutputBytes)
			if err != nil {
				return nil, err
			}
			images = append(images, image)
			if len(images) == geminiMaxImageResults {
				return images, nil
			}
		}
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("gemini image generation returned no images")
	}
	return images, nil
}

func decodeGeminiImage(encoded string, declaredMIME string, maxBytes int) (GeneratedImage, error) {
	if len(encoded) > base64.StdEncoding.EncodedLen(maxBytes) {
		return GeneratedImage{}, errGeminiImageResponseTooLarge
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return GeneratedImage{}, fmt.Errorf("decode gemini image response: %w", err)
	}
	if len(data) == 0 || len(data) > maxBytes {
		return GeneratedImage{}, errGeminiImageResponseTooLarge
	}
	mimeType, extension, ok := geminiImageType(data)
	if !ok {
		return GeneratedImage{}, fmt.Errorf("gemini image response contained an unsupported image type")
	}
	if declared := strings.ToLower(strings.TrimSpace(declaredMIME)); declared != "" && declared != mimeType {
		return GeneratedImage{}, fmt.Errorf("gemini image response MIME type did not match its bytes")
	}
	return GeneratedImage{Data: data, MimeType: mimeType, Ext: extension}, nil
}

func geminiImageType(data []byte) (mimeType string, extension string, ok bool) {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png", "png", true
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg", "jpg", true
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp", "webp", true
	default:
		return "", "", false
	}
}
