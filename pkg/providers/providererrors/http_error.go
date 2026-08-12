package providererrors

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// FromHTTPResponse normalizes a failed provider HTTP response. Only structured
// JSON identifiers determine a more specific kind than the HTTP status.
func FromHTTPResponse(status int, header http.Header, body []byte, cause error) *ProviderError {
	metadata := parseHTTPErrorBody(body)
	kind := kindFromIdentifiers(metadata.identifiers)
	if kind == KindUnknown {
		kind = kindFromHTTPStatus(status)
	}

	requestID := firstHeader(header, "X-Request-Id", "Request-Id", "X-Goog-Request-Id")
	if requestID == "" {
		requestID = metadata.requestID
	}

	retryAfter := parseRetryAfter(header.Get("Retry-After"), time.Now())
	if retryAfter == 0 {
		retryAfter = metadata.retryAfter
	}

	safeMessage := metadata.message
	if safeMessage == "" {
		safeMessage = http.StatusText(status)
	}

	providerErr := &ProviderError{
		Kind:       kind,
		HTTPStatus: status,
		RetryAfter: retryAfter,
		Cause:      cause,
	}
	return providerErr.WithRequestID(requestID).WithSafeMessage(safeMessage)
}

type httpErrorMetadata struct {
	identifiers []string
	message     string
	requestID   string
	retryAfter  time.Duration
}

func parseHTTPErrorBody(body []byte) httpErrorMetadata {
	var payload any
	if len(body) == 0 || json.Unmarshal(body, &payload) != nil {
		return httpErrorMetadata{}
	}

	root, ok := payload.(map[string]any)
	if !ok {
		return httpErrorMetadata{}
	}
	metadata := httpErrorMetadata{}
	collectHTTPErrorFields(root, &metadata)
	return metadata
}

func collectHTTPErrorFields(value any, metadata *httpErrorMetadata) {
	object, ok := value.(map[string]any)
	if !ok {
		return
	}

	for _, key := range []string{"request_id", "requestId"} {
		if metadata.requestID == "" {
			metadata.requestID = scalarString(object[key])
		}
	}
	for _, key := range []string{"quotaResetDelay", "retryDelay", "retry_after", "retryAfter"} {
		if metadata.retryAfter == 0 {
			metadata.retryAfter = parseDurationValue(object[key])
		}
	}

	if field, exists := object["error"]; exists {
		if message, ok := field.(string); ok && metadata.message == "" {
			metadata.message = message
		}
		collectHTTPErrorNested(field, metadata)
	}
	for _, key := range []string{"code", "type", "reason", "status"} {
		if identifier := scalarString(object[key]); identifier != "" {
			metadata.identifiers = append(metadata.identifiers, identifier)
		}
	}
	if message, ok := object["message"].(string); ok && metadata.message == "" {
		metadata.message = message
	}
	for _, key := range []string{"details", "metadata"} {
		collectHTTPErrorNested(object[key], metadata)
	}
}

func collectHTTPErrorNested(value any, metadata *httpErrorMetadata) {
	switch nested := value.(type) {
	case map[string]any:
		collectHTTPErrorFields(nested, metadata)
	case []any:
		for _, item := range nested {
			collectHTTPErrorNested(item, metadata)
		}
	}
}

func kindFromIdentifiers(identifiers []string) Kind {
	found := make(map[Kind]bool)
	for _, raw := range identifiers {
		identifier := canonicalIdentifier(raw)
		switch identifier {
		case "authentication_error",
			"invalid_api_key",
			"api_key_invalid",
			"unauthenticated",
			"permission_denied",
			"access_denied":
			found[KindAuthentication] = true
		case "billing_error", "insufficient_quota", "payment_required", "credit_balance_exhausted":
			found[KindBilling] = true
		case "rate_limit_error", "rate_limit_exceeded", "resource_exhausted", "quota_exceeded", "too_many_requests":
			found[KindRateLimit] = true
		case "context_length_exceeded", "context_window_exceeded", "max_tokens_exceeded", "prompt_too_long":
			found[KindContextOverflow] = true
		case "deadline_exceeded", "timeout", "request_timeout":
			found[KindTimeout] = true
		case "canceled", "cancelled": //nolint:misspell // Provider APIs may use the British spelling.
			found[KindCanceled] = true
		case "overloaded_error", "server_error", "internal", "internal_error", "unavailable", "service_unavailable":
			found[KindTransient] = true
		case "invalid_request_error", "invalid_argument", "bad_request", "not_found":
			found[KindInvalidRequest] = true
		}
	}
	for _, kind := range []Kind{
		KindAuthentication,
		KindBilling,
		KindContextOverflow,
		KindRateLimit,
		KindTimeout,
		KindCanceled,
		KindTransient,
		KindInvalidRequest,
	} {
		if found[kind] {
			return kind
		}
	}
	return KindUnknown
}

func kindFromHTTPStatus(status int) Kind {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return KindAuthentication
	case status == http.StatusPaymentRequired:
		return KindBilling
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return KindTimeout
	case status == http.StatusTooManyRequests:
		return KindRateLimit
	case status == 499:
		return KindCanceled
	case status >= 500:
		return KindTransient
	case status >= 400:
		return KindInvalidRequest
	default:
		return KindUnknown
	}
}

func canonicalIdentifier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer("-", "_", " ", "_", ".", "_")
	return replacer.Replace(value)
}

func scalarString(value any) string {
	switch scalar := value.(type) {
	case string:
		return scalar
	case float64:
		return strconv.FormatInt(int64(scalar), 10)
	default:
		return ""
	}
}

func parseDurationValue(value any) time.Duration {
	switch scalar := value.(type) {
	case string:
		duration, _ := time.ParseDuration(strings.TrimSpace(scalar))
		return duration
	case float64:
		if scalar > 0 {
			return time.Duration(scalar * float64(time.Second))
		}
	}
	return 0
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
		return 0
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}

func firstHeader(header http.Header, names ...string) string {
	for _, name := range names {
		if value := header.Get(name); value != "" {
			return value
		}
	}
	return ""
}
