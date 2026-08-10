package integrationtools

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	kagiopenapi "github.com/kagisearch/kagi-openapi-golang"
)

const (
	userAgent       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	sogouUserAgent  = "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1"
	userAgentHonest = "mintclaw/%s (+https://github.com/bogdanovich/mintclaw; AI assistant bot)"

	// HTTP client timeouts for web tool providers.
	searchTimeout     = 10 * time.Second // Brave, Tavily, DuckDuckGo
	perplexityTimeout = 30 * time.Second // Perplexity (LLM-based, slower)
	fetchTimeout      = 60 * time.Second // WebFetchTool

	defaultMaxChars = 50000
	maxRedirects    = 5
)

// Pre-compiled regexes for HTML text extraction
var (
	reScript     = regexp.MustCompile(`<script[\s\S]*?</script>`)
	reStyle      = regexp.MustCompile(`<style[\s\S]*?</style>`)
	reTags       = regexp.MustCompile(`<[^>]+>`)
	reWhitespace = regexp.MustCompile(`[^\S\n]+`)
	reBlankLines = regexp.MustCompile(`\n{3,}`)

	// DuckDuckGo result extraction
	reDDGLink = regexp.MustCompile(
		`<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>([\s\S]*?)</a>`,
	)
	reDDGSnippet = regexp.MustCompile(
		`<a class="result__snippet[^"]*".*?>([\s\S]*?)</a>`,
	)
	reSogouTitle = regexp.MustCompile(
		`<a\s+class="?resultLink"?\s+href="([^"]+)"[^>]*id="sogou_vr_\d+_\d+"[^>]*>\s*(.*?)\s*</a>`,
	)
	reSogouSnippet = regexp.MustCompile(`<div class="clamp\d*[^"]*">\s*(.*?)\s*</div>`)
	reSogouRealURL = regexp.MustCompile(`url=([^&]+)`)
)

type APIKeyPool struct {
	keys    []string
	current uint32
}

func NewAPIKeyPool(keys []string) *APIKeyPool {
	return &APIKeyPool{
		keys: keys,
	}
}

type APIKeyIterator struct {
	pool     *APIKeyPool
	startIdx uint32
	attempt  uint32
}

func (p *APIKeyPool) NewIterator() *APIKeyIterator {
	if len(p.keys) == 0 {
		return &APIKeyIterator{pool: p}
	}
	idx := atomic.AddUint32(&p.current, 1) - 1
	return &APIKeyIterator{
		pool:     p,
		startIdx: idx,
	}
}

func (it *APIKeyIterator) Next() (string, bool) {
	length := uint32(len(it.pool.keys))
	if length == 0 || it.attempt >= length {
		return "", false
	}
	key := it.pool.keys[(it.startIdx+it.attempt)%length]
	it.attempt++
	return key, true
}

type SearchProvider interface {
	Search(ctx context.Context, query string, count int, rangeCode string) (string, error)
}

type SearchResultItem struct {
	Title     string
	URL       string
	Snippet   string
	Published string
}

func extractSogouURL(href string) string {
	match := reSogouRealURL.FindStringSubmatch(href)
	if len(match) < 2 {
		return ""
	}
	decoded, err := url.QueryUnescape(match[1])
	if err != nil {
		return ""
	}
	return decoded
}

func applySogouRangeHint(query string, rangeCode string) string {
	switch rangeCode {
	case "d":
		return query + " 最近一天"
	case "w":
		return query + " 最近一周"
	case "m":
		return query + " 最近一个月"
	case "y":
		return query + " 最近一年"
	default:
		return query
	}
}

func normalizeSearchRange(raw string) (string, error) {
	rangeCode := strings.ToLower(strings.TrimSpace(raw))
	switch rangeCode {
	case "", "d", "w", "m", "y":
		return rangeCode, nil
	default:
		return "", fmt.Errorf("range must be one of: d, w, m, y")
	}
}

func mapBraveFreshness(rangeCode string) string {
	switch rangeCode {
	case "d":
		return "pd"
	case "w":
		return "pw"
	case "m":
		return "pm"
	case "y":
		return "py"
	default:
		return ""
	}
}

func mapTavilyTimeRange(rangeCode string) string {
	switch rangeCode {
	case "d":
		return "day"
	case "w":
		return "week"
	case "m":
		return "month"
	case "y":
		return "year"
	default:
		return ""
	}
}

func mapPerplexityRecencyFilter(rangeCode string) string {
	switch rangeCode {
	case "d":
		return "day"
	case "w":
		return "week"
	case "m":
		return "month"
	case "y":
		return "year"
	default:
		return ""
	}
}

func mapDuckDuckGoDateFilter(rangeCode string) string {
	switch rangeCode {
	case "d":
		return "d"
	case "w":
		return "w"
	case "m":
		return "m"
	case "y":
		return "t"
	default:
		return ""
	}
}

func mapSearXNGTimeRange(rangeCode string) string {
	switch rangeCode {
	case "d":
		return "day"
	case "w":
		return "week"
	case "m":
		return "month"
	case "y":
		return "year"
	default:
		return ""
	}
}

func mapGLMRecencyFilter(rangeCode string) string {
	switch rangeCode {
	case "d":
		return "oneDay"
	case "w":
		return "oneWeek"
	case "m":
		return "oneMonth"
	case "y":
		return "oneYear"
	default:
		return "noLimit"
	}
}

func mapBaiduRecencyFilter(rangeCode string) string {
	switch rangeCode {
	case "d", "w":
		// Baidu does not expose a day-level filter. Use the closest supported
		// window to keep recency bias instead of silently dropping the filter.
		return "week"
	case "m":
		return "month"
	case "y":
		return "year"
	default:
		return ""
	}
}

func mapKagiLensTimeFilter(rangeCode string, now time.Time) *kagiopenapi.SearchRequestLens {
	lens := kagiopenapi.NewSearchRequestLens()
	switch rangeCode {
	case "d":
		lens.SetTimeRelative("day")
	case "w":
		lens.SetTimeRelative("week")
	case "m":
		lens.SetTimeRelative("month")
	case "y":
		lens.SetTimeAfter(now.AddDate(-1, 0, 0).Format("2006-01-02"))
	default:
		return nil
	}
	return lens
}
