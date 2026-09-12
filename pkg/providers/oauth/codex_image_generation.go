package oauthprovider

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

const (
	codexDefaultImageGenerationModel = "gpt-image-2"
	codexDefaultImageGenerationSize  = "1024x1024"
	maxImageGenerationResults        = 4
	maxImageGenerationEncodedBytes   = 64 * 1024 * 1024
	maxImageGenerationResponseBytes  = maxImageGenerationEncodedBytes + 1024*1024
)

var errCodexImageResponseTooLarge = errors.New("codex image response exceeded size limit")

func (p *CodexProvider) GenerateImage(
	ctx context.Context,
	req ImageGenerationRequest,
) (*ImageGenerationResponse, error) {
	opts, accountID, err := p.requestOptions()
	if err != nil {
		return nil, err
	}
	if accountID == "" {
		return nil, &providererrors.ProviderError{
			Kind:        providererrors.KindAuthentication,
			SafeMessage: "Codex account ID is unavailable",
		}
	}

	if strings.TrimSpace(req.Model) == "" {
		req.Model = p.Capabilities().ImageGeneration.DefaultModel
	}
	if req.Count < 1 {
		req.Count = 1
	}
	if req.Count > maxImageGenerationResults {
		req.Count = maxImageGenerationResults
	}

	opts = append(opts, limitCodexImageResponseBody(maxImageGenerationResponseBytes))
	response, err := p.client.Images.Generate(ctx, buildCodexImageParams(req), opts...)
	if err != nil {
		return nil, normalizeCodexError(err)
	}
	images, err := decodeCodexImages(response, req.OutputFormat, maxImageGenerationEncodedBytes)
	if err != nil {
		return nil, err
	}
	return &ImageGenerationResponse{Images: images}, nil
}

func limitCodexImageResponseBody(maxBytes int64) option.RequestOption {
	return option.WithMiddleware(func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		response, err := next(req)
		if response != nil && response.Body != nil && maxBytes > 0 {
			response.Body = &limitedResponseBody{
				ReadCloser: response.Body,
				remaining:  maxBytes,
			}
		}
		return response, err
	})
}

type limitedResponseBody struct {
	io.ReadCloser
	remaining int64
}

func (r *limitedResponseBody) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if r.remaining < 0 {
		return 0, errCodexImageResponseTooLarge
	}
	if int64(len(buffer)) > r.remaining+1 {
		buffer = buffer[:r.remaining+1]
	}
	n, err := r.ReadCloser.Read(buffer)
	r.remaining -= int64(n)
	if r.remaining < 0 {
		return n, errCodexImageResponseTooLarge
	}
	return n, err
}

func (p *CodexProvider) requestOptions() ([]option.RequestOption, string, error) {
	var opts []option.RequestOption
	accountID := p.accountID
	if p.tokenSource != nil {
		tok, accID, err := p.tokenSource()
		if err != nil {
			return nil, "", normalizeCodexCredentialError(err)
		}
		opts = append(opts, option.WithAPIKey(tok))
		if accID != "" {
			accountID = accID
		}
	}
	if accountID != "" {
		opts = append(opts, option.WithHeader("Chatgpt-Account-Id", accountID))
	}
	return opts, accountID, nil
}

func buildCodexImageParams(req ImageGenerationRequest) openai.ImageGenerateParams {
	size := strings.TrimSpace(req.Size)
	if size == "" {
		size = codexDefaultImageGenerationSize
	}

	params := openai.ImageGenerateParams{
		Prompt: req.Prompt,
		Model:  req.Model,
		N:      openai.Opt(int64(req.Count)),
		Size:   openai.ImageGenerateParamsSize(size),
	}
	if req.Quality != "" {
		params.Quality = openai.ImageGenerateParamsQuality(req.Quality)
	}
	if req.OutputFormat != "" {
		params.OutputFormat = openai.ImageGenerateParamsOutputFormat(req.OutputFormat)
	}
	return params
}

func decodeCodexImages(
	response *openai.ImagesResponse,
	requestedFormat string,
	maxEncodedBytes int,
) ([]GeneratedImage, error) {
	if response == nil {
		return nil, fmt.Errorf("codex image generation returned no response")
	}
	outputFormat := string(response.OutputFormat)
	if strings.TrimSpace(outputFormat) == "" {
		outputFormat = requestedFormat
	}
	mime, ext := imageMimeAndExtension(outputFormat)
	limit := len(response.Data)
	if limit > maxImageGenerationResults {
		limit = maxImageGenerationResults
	}
	images := make([]GeneratedImage, 0, limit)
	totalEncodedBytes := 0
	for _, image := range response.Data[:limit] {
		totalEncodedBytes += len(image.B64JSON)
		if maxEncodedBytes > 0 && totalEncodedBytes > maxEncodedBytes {
			return nil, fmt.Errorf("codex image response exceeded size limit")
		}
		if image.B64JSON == "" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(image.B64JSON)
		if err != nil {
			return nil, fmt.Errorf("decode codex image response: %w", err)
		}
		images = append(images, GeneratedImage{Data: data, MimeType: mime, Ext: ext})
	}
	return images, nil
}

func imageMimeAndExtension(outputFormat string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(outputFormat)) {
	case "jpeg", "jpg":
		return "image/jpeg", "jpg"
	case "webp":
		return "image/webp", "webp"
	default:
		return "image/png", "png"
	}
}
