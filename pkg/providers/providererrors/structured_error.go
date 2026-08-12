package providererrors

import (
	"net/http"
	"time"
)

// FromStructuredError builds a provider error from adapter-owned structured
// metadata. A concrete kind takes precedence over the HTTP status.
func FromStructuredError(
	kind Kind,
	status int,
	header http.Header,
	requestID string,
	safeMessage string,
	cause error,
) *ProviderError {
	if kind = kind.Canonical(); kind == KindUnknown {
		kind = kindFromHTTPStatus(status)
	}
	if requestID == "" {
		requestID = firstHeader(header, "X-Request-Id", "Request-Id", "X-Goog-Request-Id")
	}

	providerErr := &ProviderError{
		Kind:       kind,
		HTTPStatus: status,
		RetryAfter: parseRetryAfter(header.Get("Retry-After"), time.Now()),
		Cause:      cause,
	}
	return providerErr.WithRequestID(requestID).WithSafeMessage(safeMessage)
}
