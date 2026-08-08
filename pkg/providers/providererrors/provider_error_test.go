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
		Kind:        Kind("custom\n" + strings.Repeat("k", 300)),
		RequestID:   strings.Repeat("r", 200),
		SafeMessage: strings.Repeat("m", 300),
	}
	got := err.Error()
	if len(got) > 470 || !strings.Contains(got, "kind=unknown") || strings.Contains(got, "custom") ||
		strings.Contains(got, "\n") {
		t.Fatalf("Error() = %q", got)
	}
}

func TestKindCanonical(t *testing.T) {
	for _, test := range []struct {
		kind Kind
		want Kind
	}{
		{kind: "", want: KindUnknown},
		{kind: KindUnknown, want: KindUnknown},
		{kind: KindBilling, want: KindBilling},
		{kind: Kind("custom"), want: KindUnknown},
	} {
		if got := test.kind.Canonical(); got != test.want {
			t.Fatalf("Canonical(%q) = %q, want %q", test.kind, got, test.want)
		}
	}
}
