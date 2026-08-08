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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}
	return NewResponse(resp, body, apiBase)
}

// NewResponse normalizes an already-buffered non-OK provider response.
func NewResponse(resp *http.Response, body []byte, apiBase string) error {
	contentType := resp.Header.Get("Content-Type")
	if common.LooksLikeHTML(body, contentType) {
		return wrapHTMLResponse(resp.StatusCode, resp.Header, body, contentType, apiBase)
	}
	cause := &common.HTTPError{
		StatusCode:  resp.StatusCode,
		BodyPreview: common.ResponsePreview(body, 128),
		ContentType: contentType,
		APIBase:     apiBase,
	}
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
		return nil, wrapHTMLResponse(resp.StatusCode, resp.Header, prefix, contentType, apiBase)
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
) error {
	cause := &common.HTTPError{
		StatusCode:  statusCode,
		BodyPreview: common.ResponsePreview(body, 128),
		ContentType: contentType,
		APIBase:     apiBase,
		IsHTML:      true,
	}
	providerErr := providererrors.FromHTTPResponse(statusCode, header, nil, cause)
	providerErr.SafeMessage = "provider returned HTML instead of JSON; check api_base or proxy configuration"
	return providerErr
}
