package gatewayclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

const MaxMediaBytes int64 = 64 * 1024 * 1024

var (
	ErrMediaUnavailable = errors.New("gateway client: bounded media unavailable")
	ErrMediaMismatch    = errors.New("gateway client: media does not match admitted metadata")
)

// MediaEvidence is caller-admitted metadata, not download or write authority.
type MediaEvidence struct {
	ContentType string
	SHA256      string
	Size        int64
}

// DownloadMedia never accepts a wire URL, follows a redirect, uses a proxy, or
// retries a request. The caller owns private retention and clearing the bytes.
func DownloadMedia(ctx context.Context, cfg *config.Config, ref string, expected MediaEvidence) ([]byte, error) {
	return DownloadMediaObserved(ctx, cfg, ref, expected, nil)
}

// DownloadMediaObserved inspects the original bounded response body before
// hashing, returning, or clearing it. The observer must not retain the slice.
func DownloadMediaObserved(
	ctx context.Context, cfg *config.Config, ref string, expected MediaEvidence, inspect func([]byte) error,
) ([]byte, error) {
	settings, err := ChannelSettings(cfg)
	if err != nil {
		return nil, ErrMediaUnavailable
	}
	endpoint, err := Endpoint(cfg, "media")
	identity, identityErr := url.Parse(ref)
	hash, hashErr := hex.DecodeString(expected.SHA256)
	contentType, _, typeErr := mime.ParseMediaType(expected.ContentType)
	if err != nil || identityErr != nil || identity.Scheme != "media" || identity.User != nil ||
		identity.Path != "" || identity.RawQuery != "" || identity.ForceQuery || identity.Fragment != "" ||
		identity.Opaque != "" || !safeMediaIdentity(identity.Host) ||
		hashErr != nil || len(hash) != sha256.Size || strings.ToLower(expected.SHA256) != expected.SHA256 ||
		typeErr != nil || contentType != expected.ContentType || expected.Size <= 0 || expected.Size > MaxMediaBytes {
		return nil, ErrMediaUnavailable
	}
	endpoint.Scheme, endpoint.Path, endpoint.RawQuery = "http", "/mintclaw/media/"+identity.Host, ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, ErrMediaUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+settings.Token.String())
	request.Header.Set("Accept", contentType)
	transport := &http.Transport{
		DialContext:            (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		DisableCompression:     true,
		DisableKeepAlives:      true,
		ResponseHeaderTimeout:  10 * time.Second,
		MaxResponseHeaderBytes: 16 * 1024,
	}
	defer transport.CloseIdleConnections()
	client := http.Client{
		Transport: transport, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, ErrMediaUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, ErrMediaUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, expected.Size+1))
	if inspect != nil {
		if inspectErr := inspect(data); inspectErr != nil {
			clear(data)
			return nil, inspectErr
		}
	}
	if err != nil {
		clear(data)
		return nil, ErrMediaUnavailable
	}
	actualType, _, typeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if typeErr != nil || actualType != contentType || response.Header.Get("Content-Encoding") != "" ||
		response.ContentLength >= 0 && response.ContentLength != expected.Size {
		clear(data)
		return nil, ErrMediaMismatch
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != expected.Size || hex.EncodeToString(sum[:]) != expected.SHA256 {
		clear(data)
		return nil, ErrMediaMismatch
	}
	return data, nil
}

func safeMediaIdentity(id string) bool {
	if id == "" || len(id) > 128 || id == "." || id == ".." {
		return false
	}
	for _, character := range id {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}
