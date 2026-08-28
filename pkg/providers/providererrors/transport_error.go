package providererrors

import (
	"context"
	"errors"
	"io"
	"net"
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
	return nil, false
}

func transportError(kind Kind, safeMessage string, cause error) *ProviderError {
	return &ProviderError{
		Kind:        kind,
		SafeMessage: safeMessage,
		Cause:       cause,
	}
}
