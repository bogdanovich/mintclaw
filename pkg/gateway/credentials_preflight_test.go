//go:build linux || darwin

package gateway

import (
	"context"
	"testing"

	"github.com/bogdanovich/mintclaw/internal/credentials/vault"
	"github.com/bogdanovich/mintclaw/pkg/browser"
)

func TestWebsiteAuthorizationNeedsNoFormOrPrivateBrowserSubmission(t *testing.T) {
	cfg := credentialGatewayFixture(t)
	cfg.Credentials.ApprovalMode = "automatic"
	cfg.Credentials.BrowserLoginEnabled, cfg.Credentials.HostID = true, "host"
	account := &cfg.Credentials.Accounts[0]
	account.DiscoverLogins, account.ItemID, account.Origins = true, "", nil
	runtime, err := setupCredentialRuntime(cfg, "fixture-operator", &credentialTestRoutes{},
		func(context.Context, string) error {
			t.Fatal("automatic preflight requested phone approval")
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	count := 1
	runtime.findLogins = func(_ context.Context, id string, query vault.DiscoveryQuery) ([]vault.Candidate, error) {
		if id != "vault" || query.Website != "https://example.org" {
			t.Fatal("preflight lost the current website or dedicated vault scope")
		}
		candidates := []vault.Candidate{
			{
				Binding: vault.Binding{
					VaultID: id,
					ItemID:  "fixture-item",
				},
				Name:    "Fixture",
				Domains: []string{"example.org"},
			},
		}
		if count == 0 {
			return nil, nil
		}
		if count == 2 {
			candidates = append(candidates, vault.Candidate{
				Binding: vault.Binding{
					VaultID: id,
					ItemID:  "other-item",
				},
				Name:    "Other",
				Domains: []string{"example.org"},
			})
		}
		return candidates, nil
	}
	runtime.consumeWebsiteLogin = func(context.Context, vault.Binding, string, string, func([]byte, []byte) error) error {
		t.Fatal("preflight retrieved private credential values")
		return nil
	}
	// No broker or form exists. Public page identity was already admitted by
	// LoginCredential; this step must finish before private browser preparation.
	source := &gatewayBrowserToolSource{services: &services{Credentials: runtime}}
	selection := browser.CredentialLoginRequest{Fields: browser.CredentialFields{Step: "authorize"}}
	page := browser.Session{SnapshotOrigin: "https://example.org"}
	principal := account.Principals[0]
	for _, scenario := range []struct {
		count   int
		item    string
		outcome string
	}{
		{0, "", "not_found"},
		{2, "", "choice_required"},
		{1, "unknown", "not_found"},
		{1, "fixture-item", "authorized"},
		{1, "", "authorized"},
	} {
		count, selection.CredentialID = scenario.count, scenario.item
		result := source.loginDiscoveredCredential(t.Context(), selection, page, principal)
		if result.Outcome != scenario.outcome || result.ApprovalWait != nil {
			t.Fatalf("preflight=%s, want %s", result.Outcome, scenario.outcome)
		}
	}
	principal.ActorID = "other"
	if result := source.loginDiscoveredCredential(t.Context(), selection, page, principal); result.Outcome != "denied" {
		t.Fatal("preflight admitted another caller")
	}
}
