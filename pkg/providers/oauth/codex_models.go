package oauthprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	// CodexDefaultModel is the bundled fallback when the authenticated catalog is unavailable.
	CodexDefaultModel            = "gpt-5.6-sol"
	CodexDefaultContextWindow    = 272_000
	CodexDefaultMaxContextWindow = 872_000
	// CodexModelsClientVersion is the Codex protocol version accepted by the model-catalog endpoint.
	CodexModelsClientVersion = "0.144.0"
	codexModelsResponseLimit = 4 * 1024 * 1024
	codexModelsTimeout       = 5 * time.Second
)

var bundledCodexModels = map[string]CodexModelInfo{
	"gpt-5.6-sol":   {Slug: "gpt-5.6-sol", ContextWindow: 272_000, MaxContextWindow: 872_000, Priority: 1},
	"gpt-5.6-terra": {Slug: "gpt-5.6-terra", ContextWindow: 272_000, MaxContextWindow: 872_000, Priority: 2},
	"gpt-5.6-luna":  {Slug: "gpt-5.6-luna", ContextWindow: 272_000, MaxContextWindow: 872_000, Priority: 3},
	"gpt-5.5":       {Slug: "gpt-5.5", ContextWindow: 272_000, MaxContextWindow: 272_000, Priority: 7},
	"gpt-5.4":       {Slug: "gpt-5.4", ContextWindow: 272_000, MaxContextWindow: 1_000_000, Priority: 16},
	"gpt-5.4-mini":  {Slug: "gpt-5.4-mini", ContextWindow: 272_000, MaxContextWindow: 272_000, Priority: 23},
	"gpt-5.3-codex-spark": {
		Slug:             "gpt-5.3-codex-spark",
		ContextWindow:    128_000,
		MaxContextWindow: 128_000,
		Priority:         26,
	},
}

// CodexModelInfo contains the catalog fields MintClaw needs for selection and context budgeting.
type CodexModelInfo struct {
	Slug             string `json:"slug"`
	ContextWindow    int    `json:"context_window"`
	MaxContextWindow int    `json:"max_context_window"`
	Priority         int    `json:"priority"`
	Visibility       string `json:"visibility"`
}

// BundledCodexModel returns fallback metadata for a known Codex model.
func BundledCodexModel(model string) (CodexModelInfo, bool) {
	model = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "openai/")
	info, ok := bundledCodexModels[model]
	return info, ok
}

// DefaultCodexModelInfo returns the bundled default model metadata.
func DefaultCodexModelInfo() CodexModelInfo {
	return bundledCodexModels[CodexDefaultModel]
}

// PreferredCodexModel selects the highest-priority visible catalog model.
func PreferredCodexModel(models []CodexModelInfo) CodexModelInfo {
	visible := make([]CodexModelInfo, 0, len(models))
	for _, model := range models {
		normalized, ok := normalizeCodexModelInfo(model)
		if !ok {
			continue
		}
		visible = append(visible, normalized)
	}
	if len(visible) == 0 {
		return DefaultCodexModelInfo()
	}
	sort.SliceStable(visible, func(left, right int) bool {
		return visible[left].Priority < visible[right].Priority
	})
	return visible[0]
}

func normalizeCodexModelInfo(model CodexModelInfo) (CodexModelInfo, bool) {
	model.Slug = strings.TrimSpace(model.Slug)
	model.Visibility = strings.TrimSpace(model.Visibility)
	if model.Visibility != "list" || model.Slug == "" || model.ContextWindow <= 0 ||
		model.MaxContextWindow < 0 ||
		strings.ContainsAny(model.Slug, " \t\n\r") || strings.HasPrefix(model.Slug, "/") ||
		strings.Contains(model.Slug, "//") ||
		(model.MaxContextWindow > 0 && model.ContextWindow > model.MaxContextWindow) {
		return CodexModelInfo{}, false
	}
	return model, true
}

// FetchCodexModels retrieves the model catalog for an authenticated ChatGPT account.
func FetchCodexModels(
	ctx context.Context,
	token, accountID string,
) ([]CodexModelInfo, error) {
	return fetchCodexModels(
		ctx,
		&http.Client{Timeout: codexModelsTimeout},
		"https://chatgpt.com/backend-api/codex/models",
		token,
		accountID,
		CodexModelsClientVersion,
	)
}

func fetchCodexModels(
	ctx context.Context,
	client *http.Client,
	endpoint, token, accountID, clientVersion string,
) ([]CodexModelInfo, error) {
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	query := requestURL.Query()
	query.Set("client_version", strings.TrimSpace(clientVersion))
	requestURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Originator", "codex_cli_rs")
	if strings.TrimSpace(accountID) != "" {
		req.Header.Set("Chatgpt-Account-Id", strings.TrimSpace(accountID))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, codexModelsResponseLimit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > codexModelsResponseLimit {
		return nil, fmt.Errorf("codex models response exceeds %d bytes", codexModelsResponseLimit)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codex models request failed with status %d", resp.StatusCode)
	}
	var payload struct {
		Models []CodexModelInfo `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode codex models response: %w", err)
	}
	return payload.Models, nil
}
