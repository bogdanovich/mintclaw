// Package githubread performs one fixed read-only API operation. Authentication
// and raw HTTP responses remain private; only bounded typed projections leave.
package githubread

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/internal/secretvalues"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

var ErrUnavailable = errors.New("private GitHub read unavailable")

type PullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Draft  bool   `json:"draft"`
}

type Client struct{ http *http.Client }

// New accepts a trusted transport for embedding/tests. The API endpoint,
// redirect policy and timeout remain fixed independently of that transport.
func New(transport http.RoundTripper) *Client {
	if transport == nil {
		transport = &http.Transport{
			DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS13},
			TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second,
			MaxResponseHeaderBytes: 64 * 1024, DisableKeepAlives: true,
			TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
		}
	}
	return &Client{http: &http.Client{
		Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// ListPullRequests never accepts an endpoint, method, header, request body or
// arbitrary API action. The caller must separately enforce its repository grant.
func (client *Client) ListPullRequests(ctx context.Context, repository string, token []byte) (
	result []PullRequest, resultErr error,
) {
	defer func() {
		if recover() != nil {
			result, resultErr = nil, ErrUnavailable
		}
	}()
	canonical, err := credentialbroker.NormalizeGitHubRepository(repository)
	if err != nil || client == nil || client.http == nil || ctx == nil || ctx.Err() != nil ||
		len(token) == 0 || len(token) > 4096 || strings.ContainsAny(string(token), "\x00\r\n") {
		return nil, ErrUnavailable
	}
	release, err := secretvalues.Register(token)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer release()
	endpoint := credentialbroker.GitHubAPIOrigin + "/repos/" + canonical + "/pulls?state=open&per_page=20"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-Github-Api-Version", "2026-03-10")
	request.Header.Set("User-Agent", "MintClaw-private-credential-connector")
	response, err := client.http.Do(request)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || response.StatusCode != http.StatusOK ||
		(media != "application/json" && media != "application/vnd.github+json") {
		return nil, ErrUnavailable
	}
	const maxBytes = 256 * 1024
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	defer clear(body)
	if err != nil || len(body) > maxBytes || ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	var items []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Draft  bool   `json:"draft"`
		State  string `json:"state"`
	}
	if json.Unmarshal(body, &items) != nil || items == nil || len(items) > 20 {
		return nil, ErrUnavailable
	}
	result = make([]PullRequest, 0, len(items))
	for _, item := range items {
		if item.Number < 1 || item.State != "open" || !utf8.ValidString(item.Title) {
			return nil, ErrUnavailable
		}
		title := secretvalues.Redact(item.Title)
		title = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, title)
		runes := []rune(title)
		if len(runes) > 500 {
			title = string(runes[:500])
		}
		result = append(result, PullRequest{
			Number: item.Number, Title: title, Draft: item.Draft,
			URL: secretvalues.Redact(fmt.Sprintf("https://github.com/%s/pull/%d", canonical, item.Number)),
		})
	}
	return result, nil
}
