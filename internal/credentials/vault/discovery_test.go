package vault

import (
	"context"
	"errors"
	"strings"
	"testing"

	onepassword "github.com/1password/onepassword-sdk-go"
)

type discoveryItems struct {
	items []onepassword.ItemOverview
	err   error
	panic bool
	calls int
}

func (items *discoveryItems) List(ctx context.Context, vaultID string,
	_ ...onepassword.ItemListFilter,
) ([]onepassword.ItemOverview, error) {
	items.calls++
	if items.panic {
		panic("synthetic-private-sdk-error")
	}
	return items.items, items.err
}

func discoveryOverview(id, title, website string) onepassword.ItemOverview {
	return onepassword.ItemOverview{
		ID: id, VaultID: "vault", Title: title,
		Category: onepassword.ItemCategoryLogin, State: onepassword.ItemStateActive,
		Websites: []onepassword.Website{{URL: website}},
	}
}

func TestDiscoveryUsesOnlyOverviewsAndProjectsDomains(t *testing.T) {
	items := &discoveryItems{items: []onepassword.ItemOverview{
		discoveryOverview("two", "Example work", "https://accounts.example.com/login?private=ignored"),
		discoveryOverview("one", "Example personal", "example.com"),
		discoveryOverview("lookalike", "Example other", "https://example.com.attacker.org"),
		discoveryOverview("private-suffix", "Tenant", "https://other.github.io"),
	}}
	candidates, err := findLogins(context.Background(), items, "vault",
		DiscoveryQuery{Website: "https://www.example.com/", Name: "EXAMPLE"})
	if err != nil || len(candidates) != 2 || candidates[0].ItemID != "one" || candidates[1].ItemID != "two" ||
		len(candidates[0].Domains) != 1 || candidates[0].Domains[0] != "example.com" || items.calls != 1 {
		t.Fatal("overview search lost deterministic name/domain matching or reference projection")
	}
	candidates, err = findLogins(context.Background(), items, "vault", DiscoveryQuery{Website: "owner.github.io"})
	if err != nil || len(candidates) != 0 {
		t.Fatal("private suffix matched a different tenant")
	}
}

func TestDiscoveryInvalidQueryDoesNotList(t *testing.T) {
	for _, query := range []DiscoveryQuery{
		{},
		{Website: "https://user:password@example.com"},
		{Website: "file:///private/path"},
		{Name: "bad\nname"},
		{Name: strings.Repeat("x", 257)},
	} {
		items := &discoveryItems{}
		if _, err := findLogins(context.Background(), items, "vault", query); err == nil || items.calls != 0 {
			t.Fatal("invalid discovery reached the provider")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	items := &discoveryItems{}
	if _, err := findLogins(ctx, items, "vault", DiscoveryQuery{Name: "example"}); err == nil || items.calls != 0 {
		t.Fatal("canceled discovery reached the provider")
	}
}

func TestDiscoveryRejectsForeignDuplicateAndUnboundedMetadata(t *testing.T) {
	valid := discoveryOverview("one", "Example", "https://example.com")
	foreign := valid
	foreign.VaultID = "other"
	large := valid
	large.Title = strings.Repeat("x", 1025)
	for _, overviews := range [][]onepassword.ItemOverview{
		{foreign}, {valid, valid}, {large}, make([]onepassword.ItemOverview, 4097),
	} {
		items := &discoveryItems{items: overviews}
		result, err := findLogins(context.Background(), items, "vault", DiscoveryQuery{Name: "Example"})
		if !errors.Is(err, ErrUnavailable) || result != nil || err.Error() != ErrUnavailable.Error() {
			t.Fatal("invalid catalog escaped as a usable candidate list")
		}
	}
}

func TestDiscoveryExcludesArchivedAndProjectsProviderFailures(t *testing.T) {
	archived := discoveryOverview("one", "Example", "example.com")
	archived.State = onepassword.ItemStateArchived
	other := archived
	other.ID, other.Category, other.State = "two", onepassword.ItemCategorySecureNote, onepassword.ItemStateActive
	items := &discoveryItems{items: []onepassword.ItemOverview{archived, other}}
	result, err := findLogins(context.Background(), items, "vault", DiscoveryQuery{Name: "Example"})
	if err != nil || len(result) != 0 {
		t.Fatal("non-active Login appeared in discovery")
	}
	for _, items := range []*discoveryItems{{err: errors.New("synthetic-private-sdk-error")}, {panic: true}} {
		result, err := findLogins(context.Background(), items, "vault", DiscoveryQuery{Name: "Example"})
		if !errors.Is(err, ErrUnavailable) || result != nil || err.Error() != ErrUnavailable.Error() {
			t.Fatal("provider failure escaped fixed discovery error")
		}
	}
}
