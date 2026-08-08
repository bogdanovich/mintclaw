package httperrors

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
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

func TestErrorBodyReadHasResourceLimits(t *testing.T) {
	t.Run("size", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", 65))),
		}

		err := handleResponse(resp, "https://api.example.com", 64, time.Second)
		if !errors.Is(err, errErrorResponseBodyTooLarge) {
			t.Fatalf("error = %v, want body-too-large cause", err)
		}
		var providerErr *providererrors.ProviderError
		if !errors.As(err, &providerErr) || providerErr.Kind != providererrors.KindTransient {
			t.Fatalf("error = %#v, want transient ProviderError", err)
		}
	})

	t.Run("idle timeout", func(t *testing.T) {
		body := newBlockingReadCloser()
		resp := &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     make(http.Header),
			Body:       body,
		}

		err := handleResponse(resp, "https://api.example.com", 64, 10*time.Millisecond)
		if !errors.Is(err, errErrorResponseReadTimeout) {
			t.Fatalf("error = %v, want read-timeout cause", err)
		}
		if !body.isClosed() {
			t.Fatal("idle timeout did not close the stalled response body")
		}
	})
}

type blockingReadCloser struct {
	closeOnce sync.Once
	closed    chan struct{}
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{closed: make(chan struct{})}
}

func (body *blockingReadCloser) Read([]byte) (int, error) {
	<-body.closed
	return 0, io.ErrClosedPipe
}

func (body *blockingReadCloser) Close() error {
	body.closeOnce.Do(func() {
		close(body.closed)
	})
	return nil
}

func (body *blockingReadCloser) isClosed() bool {
	select {
	case <-body.closed:
		return true
	default:
		return false
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
