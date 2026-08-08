package providererrors

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestProviderErrorPreservesMetadataAndCause(t *testing.T) {
	cause := errors.New("sdk response containing private diagnostics")
	err := &ProviderError{
		Kind:        KindRateLimit,
		HTTPStatus:  429,
		RetryAfter:  3 * time.Second,
		RequestID:   "req-123",
		SafeMessage: "request rate limited",
		Cause:       cause,
	}

	if !errors.Is(err, cause) {
		t.Fatal("ProviderError does not preserve its cause")
	}
	for _, want := range []string{"kind=rate_limit", "status=429", "retry_after=3s", `request_id="req-123"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Error() = %q, want %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("Error() leaked wrapped cause: %q", err.Error())
	}
}

func TestProviderErrorBoundsUserFacingFields(t *testing.T) {
	err := &ProviderError{
		RequestID:   strings.Repeat("r", 200),
		SafeMessage: strings.Repeat("m", 300),
	}
	if got := err.Error(); len(got) > 470 || !strings.Contains(got, "kind=unknown") {
		t.Fatalf("Error() = %q", got)
	}
}
