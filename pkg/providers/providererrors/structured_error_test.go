package providererrors

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestFromStructuredError(t *testing.T) {
	cause := errors.New("sdk failure")
	err := FromStructuredError(
		KindBilling,
		http.StatusTooManyRequests,
		http.Header{"Retry-After": {"17"}},
		"request\n123",
		"  credits   exhausted  ",
		cause,
	)

	if err.Kind != KindBilling {
		t.Fatalf("Kind = %q, want %q", err.Kind, KindBilling)
	}
	if err.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("HTTPStatus = %d, want %d", err.HTTPStatus, http.StatusTooManyRequests)
	}
	if err.RetryAfter != 17*time.Second {
		t.Fatalf("RetryAfter = %v, want %v", err.RetryAfter, 17*time.Second)
	}
	if err.RequestID != "request 123" {
		t.Fatalf("RequestID = %q, want %q", err.RequestID, "request 123")
	}
	if err.SafeMessage != "credits exhausted" {
		t.Fatalf("SafeMessage = %q, want %q", err.SafeMessage, "credits exhausted")
	}
	if !errors.Is(err, cause) {
		t.Fatal("structured error does not preserve its cause")
	}
}

func TestFromStructuredErrorFallsBackToStatus(t *testing.T) {
	err := FromStructuredError(KindUnknown, http.StatusServiceUnavailable, nil, "", "unavailable", nil)
	if err.Kind != KindTransient {
		t.Fatalf("Kind = %q, want %q", err.Kind, KindTransient)
	}
}
