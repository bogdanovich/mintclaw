package integrationtools

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/utils"
)

type WebSearchTool struct {
	provider         SearchProvider
	maxResults       int
	providerResolver func(query string) (SearchProvider, int)
}

type WebSearchToolOptions struct {
	Provider              string
	BraveAPIKeys          []string
	BraveMaxResults       int
	BraveEnabled          bool
	TavilyAPIKeys         []string
	TavilyBaseURL         string
	TavilyMaxResults      int
	TavilyEnabled         bool
	KagiAPIKeys           []string
	KagiBaseURL           string
	KagiMaxResults        int
	KagiEnabled           bool
	SogouMaxResults       int
	SogouEnabled          bool
	DuckDuckGoMaxResults  int
	DuckDuckGoEnabled     bool
	GeminiAPIKey          string
	GeminiModel           string
	GeminiMaxResults      int
	GeminiEnabled         bool
	PerplexityAPIKeys     []string
	PerplexityMaxResults  int
	PerplexityEnabled     bool
	SearXNGBaseURL        string
	SearXNGMaxResults     int
	SearXNGEnabled        bool
	GLMSearchAPIKey       string
	GLMSearchBaseURL      string
	GLMSearchEngine       string
	GLMSearchMaxResults   int
	GLMSearchEnabled      bool
	BaiduSearchAPIKey     string
	BaiduSearchBaseURL    string
	BaiduSearchMaxResults int
	BaiduSearchEnabled    bool
	Proxy                 string
}

func WebSearchToolOptionsFromConfig(cfg *config.Config) WebSearchToolOptions {
	return WebSearchToolOptions{
		Provider:              cfg.Tools.Web.Provider,
		BraveAPIKeys:          cfg.Tools.Web.Brave.APIKeys.Values(),
		BraveMaxResults:       cfg.Tools.Web.Brave.MaxResults,
		BraveEnabled:          cfg.Tools.Web.Brave.Enabled,
		TavilyAPIKeys:         cfg.Tools.Web.Tavily.APIKeys.Values(),
		TavilyBaseURL:         cfg.Tools.Web.Tavily.BaseURL,
		TavilyMaxResults:      cfg.Tools.Web.Tavily.MaxResults,
		TavilyEnabled:         cfg.Tools.Web.Tavily.Enabled,
		KagiAPIKeys:           cfg.Tools.Web.Kagi.APIKeys.Values(),
		KagiBaseURL:           cfg.Tools.Web.Kagi.BaseURL,
		KagiMaxResults:        cfg.Tools.Web.Kagi.MaxResults,
		KagiEnabled:           cfg.Tools.Web.Kagi.Enabled,
		SogouMaxResults:       cfg.Tools.Web.Sogou.MaxResults,
		SogouEnabled:          cfg.Tools.Web.Sogou.Enabled,
		DuckDuckGoMaxResults:  cfg.Tools.Web.DuckDuckGo.MaxResults,
		DuckDuckGoEnabled:     cfg.Tools.Web.DuckDuckGo.Enabled,
		GeminiAPIKey:          cfg.Tools.Web.Gemini.APIKey.String(),
		GeminiModel:           cfg.Tools.Web.Gemini.Model,
		GeminiMaxResults:      cfg.Tools.Web.Gemini.MaxResults,
		GeminiEnabled:         cfg.Tools.Web.Gemini.Enabled,
		PerplexityAPIKeys:     cfg.Tools.Web.Perplexity.APIKeys.Values(),
		PerplexityMaxResults:  cfg.Tools.Web.Perplexity.MaxResults,
		PerplexityEnabled:     cfg.Tools.Web.Perplexity.Enabled,
		SearXNGBaseURL:        cfg.Tools.Web.SearXNG.BaseURL,
		SearXNGMaxResults:     cfg.Tools.Web.SearXNG.MaxResults,
		SearXNGEnabled:        cfg.Tools.Web.SearXNG.Enabled,
		GLMSearchAPIKey:       cfg.Tools.Web.GLMSearch.APIKey.String(),
		GLMSearchBaseURL:      cfg.Tools.Web.GLMSearch.BaseURL,
		GLMSearchEngine:       cfg.Tools.Web.GLMSearch.SearchEngine,
		GLMSearchMaxResults:   cfg.Tools.Web.GLMSearch.MaxResults,
		GLMSearchEnabled:      cfg.Tools.Web.GLMSearch.Enabled,
		BaiduSearchAPIKey:     cfg.Tools.Web.BaiduSearch.APIKey.String(),
		BaiduSearchBaseURL:    cfg.Tools.Web.BaiduSearch.BaseURL,
		BaiduSearchMaxResults: cfg.Tools.Web.BaiduSearch.MaxResults,
		BaiduSearchEnabled:    cfg.Tools.Web.BaiduSearch.Enabled,
		Proxy:                 cfg.Tools.Web.Proxy,
	}
}

func WebSearchProviderReady(opts WebSearchToolOptions, name string) bool {
	return opts.providerReady(name)
}

func ResolveWebSearchProviderName(opts WebSearchToolOptions, query string) (string, error) {
	return opts.resolveProviderName(query)
}

var (
	knownWebSearchProviders = []string{
		"sogou",
		"duckduckgo",
		"gemini",
		"brave",
		"tavily",
		"kagi",
		"perplexity",
		"searxng",
		"glm_search",
		"baidu_search",
	}
	autoPrimaryWebSearchProviders  = []string{"perplexity", "brave", "kagi", "searxng", "tavily", "gemini"}
	autoFallbackWebSearchProviders = []string{"baidu_search", "glm_search"}
)

func isKnownWebSearchProvider(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, known := range knownWebSearchProviders {
		if name == known {
			return true
		}
	}
	return false
}

func (opts WebSearchToolOptions) providerReady(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "sogou":
		return opts.SogouEnabled
	case "duckduckgo":
		return opts.DuckDuckGoEnabled
	case "gemini":
		return opts.GeminiEnabled && strings.TrimSpace(opts.GeminiAPIKey) != ""
	case "brave":
		return opts.BraveEnabled && len(opts.BraveAPIKeys) > 0
	case "tavily":
		return opts.TavilyEnabled && len(opts.TavilyAPIKeys) > 0
	case "kagi":
		return opts.KagiEnabled && len(opts.KagiAPIKeys) > 0
	case "perplexity":
		return opts.PerplexityEnabled && len(opts.PerplexityAPIKeys) > 0
	case "searxng":
		return opts.SearXNGEnabled && strings.TrimSpace(opts.SearXNGBaseURL) != ""
	case "glm_search":
		return opts.GLMSearchEnabled && strings.TrimSpace(opts.GLMSearchAPIKey) != ""
	case "baidu_search":
		return opts.BaiduSearchEnabled && strings.TrimSpace(opts.BaiduSearchAPIKey) != ""
	default:
		return false
	}
}

func (opts WebSearchToolOptions) normalizedProviderName() string {
	providerName := strings.ToLower(strings.TrimSpace(opts.Provider))
	if providerName != "" && providerName != "auto" && !isKnownWebSearchProvider(providerName) {
		// Tolerate stale or manually edited config values at runtime by
		// treating them as "auto" and falling back to the next ready provider.
		return "auto"
	}
	return providerName
}

func (opts WebSearchToolOptions) resolveProviderName(query string) (string, error) {
	providerName := opts.normalizedProviderName()
	if providerName != "" && providerName != "auto" && opts.providerReady(providerName) {
		return providerName, nil
	}

	sogouReady := opts.providerReady("sogou")
	duckReady := opts.providerReady("duckduckgo")

	for _, name := range autoPrimaryWebSearchProviders {
		if opts.providerReady(name) {
			return name, nil
		}
	}

	if sogouReady && duckReady {
		if prefersDuckDuckGoQuery(query) {
			return "duckduckgo", nil
		}
		return "sogou", nil
	}
	if sogouReady {
		return "sogou", nil
	}
	if duckReady {
		return "duckduckgo", nil
	}

	for _, name := range autoFallbackWebSearchProviders {
		if opts.providerReady(name) {
			return name, nil
		}
	}

	return "", nil
}

func (opts WebSearchToolOptions) providerByName(name string) (SearchProvider, int, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "auto":
		return nil, 0, nil
	case "sogou":
		if !opts.providerReady("sogou") {
			return nil, 0, nil
		}
		client, err := utils.CreateHTTPClient(opts.Proxy, searchTimeout)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create HTTP client for Sogou: %w", err)
		}
		maxResults := 10
		if opts.SogouMaxResults > 0 {
			maxResults = min(opts.SogouMaxResults, 10)
		}
		return &SogouSearchProvider{
			proxy:  opts.Proxy,
			client: client,
		}, maxResults, nil
	case "perplexity":
		if !opts.providerReady("perplexity") {
			return nil, 0, nil
		}
		client, err := utils.CreateHTTPClient(opts.Proxy, perplexityTimeout)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create HTTP client for Perplexity: %w", err)
		}
		maxResults := 10
		if opts.PerplexityMaxResults > 0 {
			maxResults = min(opts.PerplexityMaxResults, 10)
		}
		return &PerplexitySearchProvider{
			keyPool: NewAPIKeyPool(opts.PerplexityAPIKeys),
			proxy:   opts.Proxy,
			client:  client,
		}, maxResults, nil
	case "brave":
		if !opts.providerReady("brave") {
			return nil, 0, nil
		}
		client, err := utils.CreateHTTPClient(opts.Proxy, searchTimeout)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create HTTP client for Brave: %w", err)
		}
		maxResults := 10
		if opts.BraveMaxResults > 0 {
			maxResults = min(opts.BraveMaxResults, 10)
		}
		return &BraveSearchProvider{
			keyPool: NewAPIKeyPool(opts.BraveAPIKeys),
			proxy:   opts.Proxy,
			client:  client,
		}, maxResults, nil
	case "gemini":
		if !opts.providerReady("gemini") {
			return nil, 0, nil
		}
		client, err := utils.CreateHTTPClient(opts.Proxy, searchTimeout)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create HTTP client for Gemini: %w", err)
		}
		maxResults := 10
		if opts.GeminiMaxResults > 0 {
			maxResults = min(opts.GeminiMaxResults, 10)
		}
		return &GeminiSearchProvider{
			apiKey: opts.GeminiAPIKey,
			model:  opts.GeminiModel,
			proxy:  opts.Proxy,
			client: client,
		}, maxResults, nil
	case "searxng":
		if !opts.providerReady("searxng") {
			return nil, 0, nil
		}
		client, err := utils.CreateHTTPClient(opts.Proxy, searchTimeout)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create HTTP client for SearXNG: %w", err)
		}
		maxResults := 10
		if opts.SearXNGMaxResults > 0 {
			maxResults = min(opts.SearXNGMaxResults, 10)
		}
		return &SearXNGSearchProvider{
			baseURL: opts.SearXNGBaseURL,
			proxy:   opts.Proxy,
			client:  client,
		}, maxResults, nil
	case "tavily":
		if !opts.providerReady("tavily") {
			return nil, 0, nil
		}
		client, err := utils.CreateHTTPClient(opts.Proxy, searchTimeout)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create HTTP client for Tavily: %w", err)
		}
		maxResults := 10
		if opts.TavilyMaxResults > 0 {
			maxResults = min(opts.TavilyMaxResults, 10)
		}
		return &TavilySearchProvider{
			keyPool: NewAPIKeyPool(opts.TavilyAPIKeys),
			baseURL: opts.TavilyBaseURL,
			proxy:   opts.Proxy,
			client:  client,
		}, maxResults, nil
	case "kagi":
		if !opts.providerReady("kagi") {
			return nil, 0, nil
		}
		client, err := utils.CreateHTTPClient(opts.Proxy, searchTimeout)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create HTTP client for Kagi: %w", err)
		}
		maxResults := 10
		if opts.KagiMaxResults > 0 {
			maxResults = min(opts.KagiMaxResults, 10)
		}
		return &KagiSearchProvider{
			keyPool: NewAPIKeyPool(opts.KagiAPIKeys),
			baseURL: opts.KagiBaseURL,
			proxy:   opts.Proxy,
			client:  client,
		}, maxResults, nil
	case "duckduckgo":
		if !opts.providerReady("duckduckgo") {
			return nil, 0, nil
		}
		client, err := utils.CreateHTTPClient(opts.Proxy, searchTimeout)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create HTTP client for DuckDuckGo: %w", err)
		}
		maxResults := 10
		if opts.DuckDuckGoMaxResults > 0 {
			maxResults = min(opts.DuckDuckGoMaxResults, 10)
		}
		return &DuckDuckGoSearchProvider{
			proxy:  opts.Proxy,
			client: client,
		}, maxResults, nil
	case "baidu_search":
		if !opts.providerReady("baidu_search") {
			return nil, 0, nil
		}
		client, err := utils.CreateHTTPClient(opts.Proxy, perplexityTimeout)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create HTTP client for Baidu Search: %w", err)
		}
		maxResults := 10
		if opts.BaiduSearchMaxResults > 0 {
			maxResults = min(opts.BaiduSearchMaxResults, 10)
		}
		return &BaiduSearchProvider{
			apiKey:  opts.BaiduSearchAPIKey,
			baseURL: opts.BaiduSearchBaseURL,
			proxy:   opts.Proxy,
			client:  client,
		}, maxResults, nil
	case "glm_search":
		if !opts.providerReady("glm_search") {
			return nil, 0, nil
		}
		client, err := utils.CreateHTTPClient(opts.Proxy, searchTimeout)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create HTTP client for GLM Search: %w", err)
		}
		searchEngine := opts.GLMSearchEngine
		if searchEngine == "" {
			searchEngine = "search_std"
		}
		maxResults := 10
		if opts.GLMSearchMaxResults > 0 {
			maxResults = min(opts.GLMSearchMaxResults, 10)
		}
		return &GLMSearchProvider{
			apiKey:       opts.GLMSearchAPIKey,
			baseURL:      opts.GLMSearchBaseURL,
			searchEngine: searchEngine,
			proxy:        opts.Proxy,
			client:       client,
		}, maxResults, nil
	default:
		return nil, 0, fmt.Errorf("unknown web search provider %q", name)
	}
}

func containsHan(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func containsLatinLetter(text string) bool {
	for _, r := range text {
		if unicode.IsLetter(r) && unicode.In(r, unicode.Latin) {
			return true
		}
	}
	return false
}

func prefersDuckDuckGoQuery(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if containsHan(trimmed) {
		return false
	}
	if containsLatinLetter(trimmed) {
		return true
	}
	return false
}

func (opts WebSearchToolOptions) buildProviderResolver() (func(query string) (SearchProvider, int), error) {
	providersByName := make(map[string]SearchProvider, len(knownWebSearchProviders))
	maxResultsByName := make(map[string]int, len(knownWebSearchProviders))

	for _, name := range knownWebSearchProviders {
		if !opts.providerReady(name) {
			continue
		}
		provider, maxResults, err := opts.providerByName(name)
		if err != nil {
			return nil, err
		}
		if provider == nil {
			continue
		}
		providersByName[name] = provider
		maxResultsByName[name] = maxResults
	}

	return func(query string) (SearchProvider, int) {
		name, err := opts.resolveProviderName(query)
		if err != nil {
			return nil, 0
		}
		provider, ok := providersByName[name]
		if !ok {
			return nil, 0
		}
		return provider, maxResultsByName[name]
	}, nil
}

func NewWebSearchTool(opts WebSearchToolOptions) (*WebSearchTool, error) {
	resolver, err := opts.buildProviderResolver()
	if err != nil {
		return nil, err
	}
	provider, maxResults := resolver("")
	if provider == nil {
		return nil, nil
	}

	return &WebSearchTool{
		provider:         provider,
		maxResults:       maxResults,
		providerResolver: resolver,
	}, nil
}

func (t *WebSearchTool) Name() string {
	return "web_search"
}

func (t *WebSearchTool) Description() string {
	return "Search the web for current information. Supports query, count, and an optional temporal range filter. Returns titles, URLs, and snippets from search results."
}

func (t *WebSearchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Search query",
			},
			"count": map[string]any{
				"type":        "integer",
				"description": "Number of results (default: 10, max: 10)",
				"minimum":     1.0,
				"maximum":     10.0,
			},
			"range": map[string]any{
				"type":        "string",
				"description": "Optional time filter: d (day), w (week), m (month), y (year)",
				"enum":        []string{"d", "w", "m", "y"},
			},
		},
		"required": []string{"query"},
	}
}

func (t *WebSearchTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	query, ok := args["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return ErrorResult("query is required")
	}
	query = strings.TrimSpace(query)

	provider := t.provider
	maxResults := t.maxResults
	if t.providerResolver != nil {
		provider, maxResults = t.providerResolver(query)
	}
	if provider == nil {
		return ErrorResult("search provider is not configured")
	}

	count64, err := getInt64Arg(args, "count", int64(maxResults))
	if err != nil {
		return ErrorResult(err.Error())
	}
	count := maxResults
	if count64 > 0 && count64 <= 10 {
		count = min(int(count64), maxResults)
	}

	rangeCode, err := normalizeSearchRange("")
	if err != nil {
		return ErrorResult(err.Error())
	}
	if rawRange, exists := args["range"]; exists {
		rangeStr, ok := rawRange.(string)
		if !ok {
			return ErrorResult("range must be a string")
		}
		rangeCode, err = normalizeSearchRange(rangeStr)
		if err != nil {
			return ErrorResult(err.Error())
		}
	}

	result, err := provider.Search(ctx, query, count, rangeCode)
	if err != nil {
		return ErrorResult(fmt.Sprintf("search failed: %v", err))
	}

	return &ToolResult{
		ForLLM:  result,
		ForUser: result,
	}
}
