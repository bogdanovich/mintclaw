//go:build bedrock

package bedrock

import (
	"errors"
	"net/http"
	"strings"

	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

func normalizeProviderError(err error) error {
	return normalizeProviderErrorWithRequestID(err, "")
}

func normalizeProviderErrorWithRequestID(err error, fallbackRequestID string) error {
	if err == nil {
		return nil
	}
	var existing *providererrors.ProviderError
	if errors.As(err, &existing) && existing != nil {
		return withRequestIDFallback(existing, err, fallbackRequestID)
	}
	if isSSOTokenError(err) {
		status, header, requestID := bedrockHTTPMetadata(err)
		if requestID == "" {
			requestID = fallbackRequestID
		}
		return providererrors.FromStructuredError(
			providererrors.KindAuthentication,
			status,
			header,
			requestID,
			"AWS credentials expired; refresh the configured SSO session",
			err,
		)
	}
	if normalized, ok := providererrors.FromTransportError(err); ok {
		return withRequestIDFallback(normalized, err, fallbackRequestID)
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		status, header, requestID := bedrockHTTPMetadata(err)
		if requestID == "" {
			requestID = fallbackRequestID
		}
		kind := bedrockErrorKind(apiErr.ErrorCode())
		if kind == providererrors.KindInvalidRequest &&
			isBedrockContextOverflow(apiErr.ErrorCode(), apiErr.ErrorMessage()) {
			kind = providererrors.KindContextOverflow
		}
		return providererrors.FromStructuredError(
			kind,
			status,
			header,
			requestID,
			apiErr.ErrorMessage(),
			err,
		)
	}

	return providererrors.FromStructuredError(
		providererrors.KindUnknown,
		0,
		nil,
		fallbackRequestID,
		"Bedrock request failed",
		err,
	)
}

func withRequestIDFallback(normalized *providererrors.ProviderError, cause error, requestID string) error {
	if normalized.RequestID != "" || requestID == "" {
		return normalized
	}
	enriched := providererrors.FromStructuredError(
		normalized.Kind,
		normalized.HTTPStatus,
		nil,
		requestID,
		normalized.SafeMessage,
		cause,
	)
	enriched.RetryAfter = normalized.RetryAfter
	return enriched
}

func bedrockHTTPMetadata(err error) (int, http.Header, string) {
	var requestID string
	var requestIDErr interface{ ServiceRequestID() string }
	if errors.As(err, &requestIDErr) {
		requestID = requestIDErr.ServiceRequestID()
	}

	var responseErr *smithyhttp.ResponseError
	if !errors.As(err, &responseErr) || responseErr == nil || responseErr.Response == nil {
		return 0, nil, requestID
	}
	header := http.Header(nil)
	if responseErr.Response.Response != nil {
		header = responseErr.Response.Response.Header
	}
	return responseErr.HTTPStatusCode(), header, requestID
}

func bedrockErrorKind(code string) providererrors.Kind {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "accessdeniedexception", "unrecognizedclientexception", "invalidsignatureexception", "expiredtokenexception":
		return providererrors.KindAuthentication
	case "accountproblem", "subscriptionrequiredexception":
		return providererrors.KindBilling
	case "throttlingexception", "toomanyrequestsexception", "servicequotaexceededexception":
		return providererrors.KindRateLimit
	case "modeltimeoutexception", "requesttimeoutexception":
		return providererrors.KindTimeout
	case "internalserverexception",
		"modelerrorexception",
		"serviceunavailableexception",
		"modelnotreadyexception",
		"modelstreamerrorexception":
		return providererrors.KindTransient
	case "conflictexception", "validationexception", "resourcenotfoundexception":
		return providererrors.KindInvalidRequest
	default:
		return providererrors.KindUnknown
	}
}

func isBedrockContextOverflow(code, message string) bool {
	if !strings.EqualFold(strings.TrimSpace(code), "ValidationException") {
		return false
	}
	message = strings.ToLower(message)
	for _, marker := range []string{
		"context length",
		"context window",
		"input is too long",
		"prompt is too long",
		"too many input tokens",
		"maximum number of input tokens",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
