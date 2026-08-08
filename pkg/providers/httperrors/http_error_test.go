package httperrors

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/providers/common"
	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

func TestInterruptedErrorBodyPreservesHTTPMetadata(t *testing.T) {
	readErr := errors.New("interrupted body")
	tests := []struct {
		name string
		call func(*http.Response) error
	}{
		{
			name: "direct error response",
			call: func(resp *http.Response) error {
				return HandleResponse(resp, "https://api.example.com")
			},
		},
		{
			name: "buffered adapter response",
			call: func(resp *http.Response) error {
				_, err := ReadResponseBody(resp, "https://api.example.com")
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header: http.Header{
					"Content-Type": {"application/json"},
					"Retry-After":  {"3"},
					"X-Request-Id": {"req-partial"},
				},
				Body: &partialErrorReadCloser{
					data: []byte(`{"error":{"type":"authentication_error","message":"Token rejected"}}`),
					err:  readErr,
				},
			}

			err := test.call(resp)
			var providerErr *providererrors.ProviderError
			if !errors.As(err, &providerErr) {
				t.Fatalf("error = %T, want ProviderError", err)
			}
			if providerErr.Kind != providererrors.KindAuthentication ||
				providerErr.HTTPStatus != http.StatusUnauthorized {
				t.Fatalf("ProviderError = %#v, want authentication status 401", providerErr)
			}
			if providerErr.RetryAfter != 3*time.Second || providerErr.RequestID != "req-partial" {
				t.Fatalf("metadata = retry %s request %q", providerErr.RetryAfter, providerErr.RequestID)
			}
			if !errors.Is(err, readErr) {
				t.Fatal("normalized error did not retain body read failure")
			}
			var httpErr *common.HTTPError
			if !errors.As(err, &httpErr) || !strings.Contains(httpErr.BodyPreview, "Token rejected") {
				t.Fatalf("normalized error lost HTTP compatibility cause: %#v", httpErr)
			}
		})
	}
}

func TestOversizedStructuredErrorPreservesClassification(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"message":"` + strings.Repeat("x", 70<<10) +
				`","type":"insufficient_quota","code":"insufficient_quota"}}`,
		)),
	}

	err := HandleResponse(resp, "https://api.example.com")
	var providerErr *providererrors.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T, want ProviderError", err)
	}
	if providerErr.Kind != providererrors.KindBilling {
		t.Fatalf("ProviderError kind = %q, want %q", providerErr.Kind, providererrors.KindBilling)
	}
	if len([]rune(providerErr.SafeMessage)) > 243 {
		t.Fatalf("SafeMessage was not bounded after classification: %d runes", len([]rune(providerErr.SafeMessage)))
	}
}

type partialErrorReadCloser struct {
	data []byte
	err  error
	done bool
}

func (reader *partialErrorReadCloser) Read(buffer []byte) (int, error) {
	if reader.done {
		return 0, reader.err
	}
	reader.done = true
	return copy(buffer, reader.data), reader.err
}

func (*partialErrorReadCloser) Close() error {
	return nil
}

var _ io.ReadCloser = (*partialErrorReadCloser)(nil)
