package oauthprovider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"

	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

func normalizeCodexError(err error) error {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) && apiErr != nil {
		status, header := codexHTTPMetadata(apiErr)
		body := []byte(apiErr.RawJSON())
		if len(body) == 0 {
			body, _ = json.Marshal(map[string]string{
				"code": apiErr.Code, "message": apiErr.Message, "param": apiErr.Param, "type": apiErr.Type,
			})
		}
		return providererrors.FromHTTPResponse(status, header, body, err)
	}
	if normalized, ok := providererrors.FromTransportError(err); ok {
		return normalized
	}
	// Streaming protocol and local parse errors have no typed SDK metadata.
	// Preserve the original cause for the shared error classifier.
	return fmt.Errorf("codex API call: %w", err)
}

func codexHTTPMetadata(apiErr *openai.Error) (int, http.Header) {
	status := apiErr.StatusCode
	header := make(http.Header)
	if apiErr.Response != nil {
		header = apiErr.Response.Header.Clone()
		if status == 0 {
			status = apiErr.Response.StatusCode
		}
	}
	return status, header
}

func normalizeCodexCredentialError(err error) error {
	if normalized, ok := providererrors.FromTransportError(err); ok {
		return normalized
	}
	return &providererrors.ProviderError{
		Kind:        providererrors.KindAuthentication,
		SafeMessage: "provider credential refresh failed",
		Cause:       err,
	}
}

func normalizeCodexResponseFailure(code, message string) error {
	cause := errors.New("codex response stream reported failure")
	body, _ := json.Marshal(map[string]string{"code": code, "message": message, "type": code})
	normalized := providererrors.FromHTTPResponse(0, nil, body, cause)
	if normalized.Kind == providererrors.KindUnknown {
		switch responses.ResponseErrorCode(code) {
		case responses.ResponseErrorCodeVectorStoreTimeout:
			normalized.Kind = providererrors.KindTimeout
		case responses.ResponseErrorCodeInvalidPrompt,
			responses.ResponseErrorCodeInvalidImage,
			responses.ResponseErrorCodeInvalidImageFormat,
			responses.ResponseErrorCodeInvalidBase64Image,
			responses.ResponseErrorCodeInvalidImageURL,
			responses.ResponseErrorCodeImageTooLarge,
			responses.ResponseErrorCodeImageTooSmall,
			responses.ResponseErrorCodeImageParseError,
			responses.ResponseErrorCodeImageContentPolicyViolation,
			responses.ResponseErrorCodeInvalidImageMode,
			responses.ResponseErrorCodeImageFileTooLarge,
			responses.ResponseErrorCodeUnsupportedImageMediaType,
			responses.ResponseErrorCodeEmptyImageFile,
			responses.ResponseErrorCodeFailedToDownloadImage,
			responses.ResponseErrorCodeImageFileNotFound:
			normalized.Kind = providererrors.KindInvalidRequest
		}
	}
	return normalized
}

func codexCanceledResponseError() error {
	return &providererrors.ProviderError{
		Kind:        providererrors.KindCanceled,
		SafeMessage: "Codex response was canceled",
	}
}

func codexIncompleteStreamError() error {
	return &providererrors.ProviderError{
		Kind:        providererrors.KindTransient,
		SafeMessage: "Codex stream ended without a final response",
		Cause:       errors.New("codex stream ended without completed response event"),
	}
}

func codexIncompleteResponseError(reason string) error {
	cause := fmt.Errorf("codex response incomplete: reason=%s", reason)
	return &providererrors.ProviderError{
		Kind:        providererrors.KindInvalidRequest,
		SafeMessage: "Codex response was incomplete",
		Cause:       cause,
	}
}
