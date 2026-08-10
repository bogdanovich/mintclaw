package integrationtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	kagiopenapi "github.com/kagisearch/kagi-openapi-golang"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

type KagiSearchProvider struct {
	keyPool *APIKeyPool
	baseURL string
	proxy   string
	client  *http.Client
}

func (p *KagiSearchProvider) Search(
	ctx context.Context,
	query string,
	count int,
	rangeCode string,
) (string, error) {
	if p.keyPool == nil || len(p.keyPool.keys) == 0 {
		return "", errors.New("no API key provided")
	}

	client := p.client
	if client == nil {
		client = &http.Client{Timeout: searchTimeout}
	}

	apiClient := newKagiAPIClient(client, p.baseURL)
	searchReq := kagiopenapi.NewSearchRequest(query)
	searchReq.SetLimit(int32(count))
	if lens := mapKagiLensTimeFilter(rangeCode, time.Now().UTC()); lens != nil {
		searchReq.SetLens(*lens)
	}

	var lastErr error
	iter := p.keyPool.NewIterator()

	for {
		apiKey, ok := iter.Next()
		if !ok {
			break
		}

		authCtx := context.WithValue(ctx, kagiopenapi.ContextAccessToken, apiKey)
		searchResp, httpResp, err := apiClient.SearchAPI.Search(authCtx).SearchRequest(*searchReq).Execute()
		if httpResp != nil && httpResp.Body != nil {
			defer func() { _ = httpResp.Body.Close() }()
		}
		if err != nil {
			if httpResp != nil {
				if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
					results, parseErr := fallbackKagiSearchResults(httpResp, count)
					if parseErr != nil {
						return "", parseErr
					}
					return formatKagiSearchResults(query, results), nil
				}
				lastErr = kagiStatusError(httpResp.StatusCode)
				if httpResp.StatusCode == http.StatusTooManyRequests ||
					httpResp.StatusCode == http.StatusUnauthorized ||
					httpResp.StatusCode == http.StatusForbidden ||
					httpResp.StatusCode >= 500 {
					continue
				}
				return "", lastErr
			}
			lastErr = fmt.Errorf("request failed: %w", err)
			continue
		}

		results := kagiSearchResults(searchResp, count)
		if len(results) == 0 {
			return fmt.Sprintf("No results for: %s", query), nil
		}

		return formatKagiSearchResults(query, results), nil
	}

	return "", fmt.Errorf("all api keys failed, last error: %w", lastErr)
}

func newKagiAPIClient(client *http.Client, baseURL string) *kagiopenapi.APIClient {
	cfg := kagiopenapi.NewConfiguration()
	cfg.UserAgent = fmt.Sprintf(userAgentHonest, config.Version)
	cfg.HTTPClient = client
	cfg.Servers = kagiopenapi.ServerConfigurations{{
		URL:         kagiServerURL(baseURL),
		Description: "Kagi Search API endpoint",
	}}
	return kagiopenapi.NewAPIClient(cfg)
}

func formatKagiSearchResults(query string, results []SearchResultItem) string {
	if len(results) == 0 {
		return fmt.Sprintf("No results for: %s", query)
	}
	lines := []string{fmt.Sprintf("Results for: %s (via Kagi)", query)}
	for i, item := range results {
		title := item.Title
		if title == "" {
			title = item.URL
		}
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, title, item.URL))
		if item.Published != "" {
			lines = append(lines, fmt.Sprintf("   Published: %s", item.Published))
		}
		if item.Snippet != "" {
			lines = append(lines, fmt.Sprintf("   %s", item.Snippet))
		}
	}
	return strings.Join(lines, "\n")
}

func kagiServerURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "https://kagi.com/api/v1"
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimSuffix(baseURL, "/")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.Path = strings.TrimSuffix(parsed.Path, "/search")
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return strings.TrimRight(parsed.String(), "/")
}

func kagiStatusError(statusCode int) error {
	switch statusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("kagi Search API authentication failed (status %d)", statusCode)
	case http.StatusForbidden:
		return fmt.Errorf("kagi Search API request forbidden (status %d)", statusCode)
	case http.StatusTooManyRequests:
		return fmt.Errorf("kagi Search API rate limited (status %d)", statusCode)
	default:
		if statusCode >= 500 {
			return fmt.Errorf("kagi Search API server error (status %d)", statusCode)
		}
		return fmt.Errorf("kagi Search API error (status %d)", statusCode)
	}
}

type kagiFallbackResult struct {
	Type      int    `json:"t"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	Snippet   string `json:"snippet"`
	Time      string `json:"time"`
	Published string `json:"published"`
}

func fallbackKagiSearchResults(resp *http.Response, count int) ([]SearchResultItem, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("failed to parse response: empty response body")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	return parseFallbackKagiSearchResults(body, count)
}

func parseFallbackKagiSearchResults(body []byte, count int) ([]SearchResultItem, error) {
	if count <= 0 {
		count = 10
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	data := bytes.TrimSpace(envelope.Data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, nil
	}

	results := make([]SearchResultItem, 0, count)
	switch data[0] {
	case '{':
		var modern struct {
			Search []kagiFallbackResult `json:"search"`
		}
		if err := json.Unmarshal(data, &modern); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}
		appendFallbackKagiResults(&results, modern.Search, count, false)
	case '[':
		var legacy []kagiFallbackResult
		if err := json.Unmarshal(data, &legacy); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}
		appendFallbackKagiResults(&results, legacy, count, true)
	default:
		return nil, fmt.Errorf("failed to parse response: unexpected data shape")
	}
	return results, nil
}

func appendFallbackKagiResults(
	results *[]SearchResultItem,
	items []kagiFallbackResult,
	count int,
	requireLegacyType bool,
) {
	for _, item := range items {
		if len(*results) >= count {
			return
		}
		if requireLegacyType && item.Type != 0 {
			continue
		}
		urlStr := strings.TrimSpace(item.URL)
		if urlStr == "" {
			continue
		}
		published := strings.TrimSpace(item.Published)
		if published == "" {
			published = strings.TrimSpace(item.Time)
		}
		*results = append(*results, SearchResultItem{
			Title:     cleanSearchText(item.Title),
			URL:       urlStr,
			Snippet:   cleanSearchText(item.Snippet),
			Published: published,
		})
	}
}

func kagiSearchResults(searchResp *kagiopenapi.Search200Response, count int) []SearchResultItem {
	if count <= 0 {
		count = 10
	}
	if searchResp == nil || searchResp.Data == nil {
		return nil
	}

	results := make([]SearchResultItem, 0, count)
	for _, item := range searchResp.Data.Search {
		if len(results) >= count {
			break
		}
		urlStr := strings.TrimSpace(item.GetUrl())
		if urlStr == "" {
			continue
		}
		results = append(results, SearchResultItem{
			Title:     cleanSearchText(item.GetTitle()),
			URL:       urlStr,
			Snippet:   cleanSearchText(item.GetSnippet()),
			Published: strings.TrimSpace(item.GetTime()),
		})
	}
	return results
}

func cleanSearchText(content string) string {
	return strings.TrimSpace(html.UnescapeString(stripTags(content)))
}
