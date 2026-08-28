package anthropicprovider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

func normalizeAnthropicError(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) && apiErr != nil {
		status, header := anthropicHTTPMetadata(apiErr)
		raw := []byte(apiErr.RawJSON())
		normalized := providererrors.FromHTTPResponse(status, header, raw, err)
		refineAnthropicContextError(normalized, raw)
		return normalized
	}
	if normalized, ok := providererrors.FromTransportError(err); ok {
		return normalized
	}
	// The SDK can surface local decode/stream failures without typed metadata.
	// Preserve the original cause for the shared error classifier.
	return fmt.Errorf("claude API call: %w", err)
}

func anthropicHTTPMetadata(apiErr *anthropic.Error) (int, http.Header) {
	status := apiErr.StatusCode
	header := make(http.Header)
	if apiErr.Response != nil {
		header = apiErr.Response.Header.Clone()
		if status == 0 {
			status = apiErr.Response.StatusCode
		}
	}
	if header.Get("Request-Id") == "" && apiErr.RequestID != "" {
		header.Set("Request-Id", apiErr.RequestID)
	}
	return status, header
}

func normalizeAnthropicCredentialError(err error) error {
	if normalized, ok := providererrors.FromTransportError(err); ok {
		return normalized
	}
	return &providererrors.ProviderError{
		Kind:        providererrors.KindAuthentication,
		SafeMessage: "provider credential refresh failed",
		Cause:       err,
	}
}

func refineAnthropicContextError(normalized *providererrors.ProviderError, raw []byte) {
	if normalized == nil || normalized.Kind != providererrors.KindInvalidRequest {
		return
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return
	}
	message := strings.ToLower(envelope.Error.Message)
	for _, marker := range []string{"prompt is too long", "maximum context length", "context window", "too many tokens"} {
		if strings.Contains(message, marker) {
			normalized.Kind = providererrors.KindContextOverflow
			return
		}
	}
}
