package vault

import (
	"context"
	"net/url"
	"runtime"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	onepassword "github.com/1password/onepassword-sdk-go"
	"golang.org/x/net/publicsuffix"
)

// DiscoveryQuery searches item overviews during private operator setup. A
// website match suggests candidates; it never adds runtime origin authority.
type DiscoveryQuery struct {
	Website string
	Name    string
}

// Candidate contains references and display metadata, never credential fields.
type Candidate struct {
	Binding
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
}

type itemLister interface {
	List(context.Context, string, ...onepassword.ItemListFilter) ([]onepassword.ItemOverview, error)
}

// FindLogins uses only the SDK overview-list operation. Its token must come
// from protected operator storage, never an argument, environment, or model.
func FindLogins(ctx context.Context, token []byte, vaultID string, query DiscoveryQuery) ([]Candidate, error) {
	if ctx == nil {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	provider, err := New(ctx, token)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer runtime.KeepAlive(provider.client)
	return findLogins(ctx, provider.client.Items(), vaultID, query)
}

func findLogins(ctx context.Context, items itemLister, vaultID string,
	query DiscoveryQuery,
) (result []Candidate, resultErr error) {
	defer func() {
		if recover() != nil {
			result, resultErr = nil, ErrUnavailable
		}
	}()
	name := strings.TrimSpace(query.Name)
	if ctx == nil || ctx.Err() != nil || items == nil || !validProviderID(vaultID) ||
		(name == "" && query.Website == "") || len(name) > 256 || !utf8.ValidString(name) ||
		strings.ContainsFunc(name, unicode.IsControl) {
		return nil, ErrUnavailable
	}
	domain := ""
	if query.Website != "" {
		var ok bool
		domain, ok = websiteDomain(query.Website)
		if !ok {
			return nil, ErrUnavailable
		}
	}
	requestCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	overviews, err := items.List(requestCtx, vaultID)
	if err != nil || requestCtx.Err() != nil || len(overviews) > 4096 {
		return nil, ErrUnavailable
	}
	seen := make(map[string]bool, len(overviews))
	for _, item := range overviews {
		if item.VaultID != vaultID || !validProviderID(item.ID) || seen[item.ID] {
			return nil, ErrUnavailable
		}
		seen[item.ID] = true
		if item.Category != onepassword.ItemCategoryLogin || item.State != onepassword.ItemStateActive {
			continue
		}
		if len(item.Title) > 1024 || !utf8.ValidString(item.Title) || len(item.Websites) > 64 {
			return nil, ErrUnavailable
		}
		if name != "" && !strings.Contains(strings.ToLower(item.Title), strings.ToLower(name)) {
			continue
		}
		domains := make([]string, 0, len(item.Websites))
		for _, website := range item.Websites {
			if d, ok := websiteDomain(website.URL); ok && !slices.Contains(domains, d) {
				domains = append(domains, d)
			}
		}
		if domain != "" && !slices.Contains(domains, domain) {
			continue
		}
		slices.Sort(domains)
		result = append(result, Candidate{
			Binding: Binding{VaultID: vaultID, ItemID: item.ID},
			Name:    item.Title, Domains: domains,
		})
		if len(result) > 20 {
			return nil, ErrUnavailable
		}
	}
	slices.SortFunc(result, func(a, b Candidate) int {
		if order := strings.Compare(a.Name, b.Name); order != 0 {
			return order
		}
		return strings.Compare(a.ItemID, b.ItemID)
	})
	return result, nil
}

func websiteDomain(raw string) (string, bool) {
	if len(raw) == 0 || len(raw) > 2048 || strings.ContainsAny(raw, "\x00\r\n\\") {
		return "", false
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil {
		return "", false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	domain, err := publicsuffix.EffectiveTLDPlusOne(host)
	return domain, err == nil
}
