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
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

const (
	codexDefaultImageGenerationModel = "gpt-image-2"
	codexDefaultImageGenerationSize  = "1024x1024"
	codexImageResponsesModel         = "gpt-5.5"
	codexImageResponsesInstructions  = "You are an image generation assistant."
	maxImageGenerationResults        = 4
	maxImageEditInputs               = 4
	maxImageEditInputBytes           = 50*1024*1024 - 1
	maxImageGenerationEncodedBytes   = 64 * 1024 * 1024
	maxImageGenerationResponseBytes  = maxImageGenerationEncodedBytes + 1024*1024
)

var errCodexImageResponseTooLarge = errors.New("codex image response exceeded size limit")

func (p *CodexProvider) GenerateImage(
	ctx context.Context,
	req ImageGenerationRequest,
) (*ImageGenerationResponse, error) {
	if err := validateCodexImageEditInputs(req.InputImages); err != nil {
		return nil, err
	}
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
	if len(req.InputImages) > 0 {
		return p.generateImageEditViaResponses(ctx, req, opts)
	}
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

func validateCodexImageEditInputs(inputs []ImageGenerationInput) error {
	if len(inputs) > maxImageEditInputs {
		return fmt.Errorf("codex image edit exceeded input image limit")
	}
	for index, input := range inputs {
		if len(input.Data) > maxImageEditInputBytes {
			return fmt.Errorf("codex image edit input %d exceeded byte limit", index+1)
		}
	}
	return nil
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

func (p *CodexProvider) generateImageEditViaResponses(
	ctx context.Context,
	req ImageGenerationRequest,
	opts []option.RequestOption,
) (*ImageGenerationResponse, error) {
	desiredResults := min(req.Count, maxImageGenerationResults)
	encodedImages := make([]string, 0, desiredResults)
	for range desiredResults {
		encoded, err := p.generateOneImageEditViaResponses(ctx, req, opts)
		if err != nil {
			return nil, err
		}
		encodedImages = append(encodedImages, encoded...)
		if len(encodedImages) >= desiredResults {
			encodedImages = encodedImages[:desiredResults]
			break
		}
	}
	images, err := decodeCodexImagePayloads(encodedImages, req.OutputFormat, maxImageGenerationEncodedBytes)
	if err != nil {
		return nil, err
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("codex image edit returned no images")
	}
	return &ImageGenerationResponse{Images: images}, nil
}

func (p *CodexProvider) generateOneImageEditViaResponses(
	ctx context.Context,
	req ImageGenerationRequest,
	opts []option.RequestOption,
) ([]string, error) {
	params := buildCodexImageEditResponseParams(req)
	stream := p.client.Responses.NewStreaming(ctx, params, opts...)
	defer func() { _ = stream.Close() }()

	var completed *responses.Response
	outputItemImages := make([]string, 0, 1)
	for stream.Next() {
		event := stream.Current()
		switch event.Type {
		case "error":
			return nil, normalizeCodexResponseFailure(event.Code, event.Message)
		case "response.output_item.done":
			item := event.AsResponseOutputItemDone().Item
			if item.Type == "image_generation_call" {
				imageCall := item.AsImageGenerationCall()
				if imageCall.Result != "" {
					outputItemImages = append(outputItemImages, imageCall.Result)
				}
			}
		case "response.completed", "response.failed", "response.incomplete":
			response := event.Response
			if response.ID != "" {
				completed = &response
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, normalizeCodexError(err)
	}
	if completed == nil {
		return nil, codexIncompleteStreamError()
	}
	switch completed.Status {
	case responses.ResponseStatusCompleted:
	case responses.ResponseStatusFailed:
		return nil, normalizeCodexResponseFailure(string(completed.Error.Code), completed.Error.Message)
	case responses.ResponseStatusCancelled:
		return nil, codexCanceledResponseError()
	case responses.ResponseStatusIncomplete:
		return nil, codexIncompleteResponseError(completed.IncompleteDetails.Reason)
	default:
		return nil, codexIncompleteStreamError()
	}
	if len(outputItemImages) > 0 {
		return outputItemImages, nil
	}
	completedImages := make([]string, 0, 1)
	for _, item := range completed.Output {
		if item.Type != "image_generation_call" {
			continue
		}
		imageCall := item.AsImageGenerationCall()
		if imageCall.Result != "" {
			completedImages = append(completedImages, imageCall.Result)
		}
	}
	return completedImages, nil
}

func buildCodexImageEditResponseParams(req ImageGenerationRequest) responses.ResponseNewParams {
	content := responses.ResponseInputMessageContentListParam{
		responses.ResponseInputContentParamOfInputText(req.Prompt),
	}
	for _, input := range req.InputImages {
		contentType := strings.TrimSpace(input.ContentType)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		image := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
		image.OfInputImage.ImageURL = openai.String(
			"data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(input.Data),
		)
		content = append(content, image)
	}
	tool := responses.ToolImageGenerationParam{
		Action:        "edit",
		InputFidelity: req.InputFidelity,
		Model:         req.Model,
		OutputFormat:  req.OutputFormat,
		Quality:       req.Quality,
		Size:          req.Size,
	}
	return responses.ResponseNewParams{
		Instructions: openai.String(codexImageResponsesInstructions),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: responses.ResponseInputParam{
				responses.ResponseInputItemParamOfMessage(content, responses.EasyInputMessageRoleUser),
			},
		},
		Model: shared.ResponsesModel(codexImageResponsesModel),
		Store: openai.Bool(false),
		ToolChoice: responses.ResponseNewParamsToolChoiceUnion{
			OfHostedTool: &responses.ToolChoiceTypesParam{
				Type: responses.ToolChoiceTypesTypeImageGeneration,
			},
		},
		Tools: []responses.ToolUnionParam{{OfImageGeneration: &tool}},
	}
}

func decodeCodexImagePayloads(
	encodedImages []string,
	requestedFormat string,
	maxEncodedBytes int,
) ([]GeneratedImage, error) {
	mime, ext := imageMimeAndExtension(requestedFormat)
	return decodeCodexImagePayloadsWithType(encodedImages, mime, ext, maxEncodedBytes)
}

func decodeCodexImagePayloadsWithType(
	encodedImages []string,
	mime string,
	ext string,
	maxEncodedBytes int,
) ([]GeneratedImage, error) {
	images := make([]GeneratedImage, 0, len(encodedImages))
	totalEncodedBytes := 0
	for _, encoded := range encodedImages {
		totalEncodedBytes += len(encoded)
		if maxEncodedBytes > 0 && totalEncodedBytes > maxEncodedBytes {
			return nil, fmt.Errorf("codex image response exceeded size limit")
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode codex image response: %w", err)
		}
		images = append(images, GeneratedImage{Data: data, MimeType: mime, Ext: ext})
	}
	return images, nil
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
	encodedImages := make([]string, 0, limit)
	for _, image := range response.Data[:limit] {
		if image.B64JSON == "" {
			continue
		}
		encodedImages = append(encodedImages, image.B64JSON)
	}
	return decodeCodexImagePayloadsWithType(encodedImages, mime, ext, maxEncodedBytes)
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
