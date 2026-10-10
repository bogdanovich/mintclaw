package providererrors

import (
	"context"
	"errors"
	"io"
	"net"

	"golang.org/x/net/http2"
)

// FromTransportError normalizes concrete transport and context failures. It
// returns false when an adapter must preserve the original error for further classification.
func FromTransportError(err error) (*ProviderError, bool) {
	if err == nil {
		return nil, false
	}
	var providerErr *ProviderError
	if errors.As(err, &providerErr) && providerErr != nil {
		return providerErr, true
	}

	switch {
	case errors.Is(err, context.Canceled):
		return transportError(KindCanceled, "provider request canceled", err), true
	case errors.Is(err, context.DeadlineExceeded):
		return transportError(KindTimeout, "provider request timed out", err), true
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return transportError(KindNetwork, "provider connection closed unexpectedly", err), true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return transportError(KindTimeout, "provider request timed out", err), true
		}
		return transportError(KindNetwork, "provider network request failed", err), true
	}

	// net/http exposes its internal HTTP/2 stream error through a structural
	// errors.As conversion to x/net/http2.StreamError. INTERNAL_ERROR is a
	// peer-side failure and REFUSED_STREAM explicitly means the request was not
	// processed, so both can use the bounded request retry/fallback path.
	var streamErr http2.StreamError
	if errors.As(err, &streamErr) &&
		(streamErr.Code == http2.ErrCodeInternal || streamErr.Code == http2.ErrCodeRefusedStream) {
		return transportError(KindNetwork, "provider HTTP/2 stream failed", err), true
	}
	return nil, false
}

func transportError(kind Kind, safeMessage string, cause error) *ProviderError {
	return &ProviderError{
		Kind:        kind,
		SafeMessage: safeMessage,
		Cause:       cause,
	}
}
