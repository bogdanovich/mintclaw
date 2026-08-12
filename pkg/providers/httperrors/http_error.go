// Package httperrors normalizes provider HTTP failures without adding provider
// runtime dependencies to the broadly shared common package.
package httperrors

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/providers/common"
	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

const (
	maxErrorResponseBodyBytes    = 1 << 20
	errorResponseReadIdleTimeout = 30 * time.Second
)

var (
	errErrorResponseBodyTooLarge = errors.New("provider error response body exceeds limit")
	errErrorResponseReadTimeout  = errors.New("provider error response body read timed out")
)

// HandleResponse reads and normalizes a non-OK provider response.
func HandleResponse(resp *http.Response, apiBase string) error {
	return handleResponse(resp, apiBase, maxErrorResponseBodyBytes, errorResponseReadIdleTimeout)
}

func handleResponse(resp *http.Response, apiBase string, maxBytes int64, idleTimeout time.Duration) error {
	body, readErr := readErrorResponseBody(resp.Body, maxBytes, idleTimeout)
	return newResponse(resp, body, apiBase, readErr)
}

// NewResponse normalizes an already-buffered non-OK provider response.
func NewResponse(resp *http.Response, body []byte, apiBase string) error {
	return newResponse(resp, body, apiBase, nil)
}

// ReadResponseBody reads a response expected to return HTTP 200. A non-OK
// response is normalized even when reading its body also fails.
func ReadResponseBody(resp *http.Response, apiBase string) ([]byte, error) {
	if resp.StatusCode != http.StatusOK {
		body, readErr := readErrorResponseBody(
			resp.Body,
			maxErrorResponseBodyBytes,
			errorResponseReadIdleTimeout,
		)
		return nil, newResponse(resp, body, apiBase, readErr)
	}
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("reading response body: %w", readErr)
	}
	return body, nil
}

func readErrorResponseBody(body io.ReadCloser, maxBytes int64, idleTimeout time.Duration) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	reader := &errorResponseIdleTimeoutBody{body: body, timeout: idleTimeout}
	data, readErr := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if int64(len(data)) <= maxBytes {
		return data, readErr
	}
	return data[:maxBytes], errors.Join(readErr, errErrorResponseBodyTooLarge)
}

type errorResponseIdleTimeoutBody struct {
	body    io.ReadCloser
	timeout time.Duration
}

func (body *errorResponseIdleTimeoutBody) Read(buffer []byte) (int, error) {
	if body.timeout <= 0 {
		return body.body.Read(buffer)
	}
	timedOut := make(chan struct{})
	timer := time.AfterFunc(body.timeout, func() {
		_ = body.body.Close()
		close(timedOut)
	})
	count, readErr := body.body.Read(buffer)
	if timer.Stop() {
		return count, readErr
	}
	<-timedOut
	return count, errors.Join(errErrorResponseReadTimeout, readErr)
}

func newResponse(resp *http.Response, body []byte, apiBase string, readErr error) error {
	contentType := resp.Header.Get("Content-Type")
	if common.LooksLikeHTML(body, contentType) {
		return wrapHTMLResponse(resp.StatusCode, resp.Header, body, contentType, apiBase, readErr)
	}
	httpCause := &common.HTTPError{
		StatusCode:  resp.StatusCode,
		BodyPreview: common.ResponsePreview(body, 128),
		ContentType: contentType,
		APIBase:     apiBase,
	}
	cause := withReadError(httpCause, readErr)
	return providererrors.FromHTTPResponse(resp.StatusCode, resp.Header, body, cause)
}

// ReadAndParseResponse detects HTML before decoding an OpenAI-compatible body.
func ReadAndParseResponse(resp *http.Response, apiBase string) (*common.LLMResponse, error) {
	contentType := resp.Header.Get("Content-Type")
	reader := bufio.NewReader(resp.Body)
	prefix, err := reader.Peek(256)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, fmt.Errorf("failed to inspect response: %w", err)
	}
	if common.LooksLikeHTML(prefix, contentType) {
		return nil, wrapHTMLResponse(resp.StatusCode, resp.Header, prefix, contentType, apiBase, nil)
	}
	out, err := common.ParseResponse(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}
	return out, nil
}

func wrapHTMLResponse(
	statusCode int,
	header http.Header,
	body []byte,
	contentType string,
	apiBase string,
	readErr error,
) error {
	httpCause := &common.HTTPError{
		StatusCode:  statusCode,
		BodyPreview: common.ResponsePreview(body, 128),
		ContentType: contentType,
		APIBase:     apiBase,
		IsHTML:      true,
	}
	cause := withReadError(httpCause, readErr)
	providerErr := providererrors.FromHTTPResponse(statusCode, header, nil, cause)
	return providerErr.WithSafeMessage(
		"provider returned HTML instead of JSON; check api_base or proxy configuration",
	)
}

func withReadError(httpCause *common.HTTPError, readErr error) error {
	if readErr == nil {
		return httpCause
	}
	return errors.Join(httpCause, fmt.Errorf("reading response body: %w", readErr))
}
