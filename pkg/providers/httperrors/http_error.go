// Package httperrors normalizes provider HTTP failures without adding provider
// runtime dependencies to the broadly shared common package.
package httperrors

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/bogdanovich/mintclaw/pkg/providers/common"
	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

// HandleResponse reads and normalizes a non-OK provider response.
func HandleResponse(resp *http.Response, apiBase string) error {
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return newResponse(resp, body, apiBase, readErr)
}

// NewResponse normalizes an already-buffered non-OK provider response.
func NewResponse(resp *http.Response, body []byte, apiBase string) error {
	return newResponse(resp, body, apiBase, nil)
}

// ReadResponseBody reads a response expected to return HTTP 200. A non-OK
// response is normalized even when reading its body also fails.
func ReadResponseBody(resp *http.Response, apiBase string) ([]byte, error) {
	body, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, newResponse(resp, body, apiBase, readErr)
	}
	if readErr != nil {
		return nil, fmt.Errorf("reading response body: %w", readErr)
	}
	return body, nil
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
	providerErr.SafeMessage = "provider returned HTML instead of JSON; check api_base or proxy configuration"
	return providerErr
}

func withReadError(httpCause *common.HTTPError, readErr error) error {
	if readErr == nil {
		return httpCause
	}
	return errors.Join(httpCause, fmt.Errorf("reading response body: %w", readErr))
}
