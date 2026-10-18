package approval

import (
	"testing"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

func TestDiscoveredLoginRequiresSignedGrantAndNarrowsToOneItemAndDomain(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	rules := []credentialbroker.AccountRule{{
		Alias: "facebook", VaultID: "private-vault", DiscoverLogins: true,
		Principals: []credentialbroker.Principal{testPrincipal},
	}}
	if fixture.controller.ReplacePolicy(t.Context(), rules) != nil {
		t.Fatal("discovery policy fixture failed")
	}
	request, vaultID, err := fixture.controller.DiscoveryRequest(t.Context(), testPrincipal,
		"facebook", "https://owner.github.io")
	if err != nil || vaultID != "private-vault" {
		t.Fatal("trusted metadata discovery unavailable")
	}
	if _, err = fixture.controller.ActiveLogin(t.Context(), request); err == nil {
		t.Fatal("metadata discovery granted value access")
	}
	_, ceremony := fixture.approval(t, credentialbroker.MaxGrantLifetime)
	if !ceremony.Scope.VaultLoginAccess {
		t.Fatal("phone did not disclose vault-wide Login access")
	}
	grant, err := fixture.controller.FinishApproval(t.Context(), ceremony.ID,
		fixture.assertion(t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false))
	if err != nil {
		t.Fatal("signed discovery approval failed")
	}
	lease, err := fixture.controller.ActiveLogin(t.Context(), request)
	if err != nil {
		t.Fatal("signed discovery grant unavailable")
	}
	if _, err = lease.Binding(t.Context(), request); err == nil {
		t.Fatal("vault-wide lease invented an exact item")
	}
	if _, err = fixture.controller.AuthorizedBinding(t.Context(), grant.ID, request); err == nil {
		t.Fatal("unnarrowed discovery grant exposed a provider binding")
	}
	bound, err := lease.BindDiscovered(t.Context(), request, "discovered-item")
	binding, bindErr := bound.Binding(t.Context(), request)
	if err != nil || bindErr != nil || binding.ItemID != "discovered-item" || binding.VaultID != vaultID {
		t.Fatal("discovered lease lost its exact immutable item")
	}
	other := request
	other.Origin = "https://accounts.owner.github.io"
	if bound.Check(t.Context(), other) != nil {
		t.Fatal("same website subdomain was rejected")
	}
	for _, origin := range []string{
		"https://other.github.io", "https://owner.github.io.attacker.org", "http://owner.github.io",
	} {
		other.Origin = origin
		if bound.Check(t.Context(), other) == nil {
			t.Fatal("narrowed lease crossed website/domain authority")
		}
	}
	if _, err = bound.BindDiscovered(t.Context(), request, "replacement-item"); err == nil {
		t.Fatal("narrowed lease could be rebound to another item")
	}
	other = request
	other.Origin = "https://another.example.com"
	if _, err = fixture.controller.ActiveLogin(t.Context(), other); err != nil {
		t.Fatal("vault-wide grant could not be reused for another matched website")
	}
	revocation, err := fixture.controller.BeginRevocation(t.Context(), grant.ID)
	if err != nil || !revocation.Scope.VaultLoginAccess {
		t.Fatal("revocation lost the disclosed vault scope")
	}
	if err = fixture.controller.FinishRevocation(
		t.Context(),
		revocation.ID,
		fixture.assertion(
			t,
			revocation,
			testOrigin,
			protocol.FlagUserPresent|protocol.FlagUserVerified,
			2,
			false,
		),
	); err != nil {
		t.Fatal("signed discovery revocation failed")
	}
	select {
	case <-bound.Done():
	default:
		t.Fatal("revoked discovery grant did not retire its narrowed consumers")
	}
	if _, err = bound.Binding(t.Context(), request); err == nil {
		t.Fatal("revoked discovered binding remained usable")
	}
}

func TestDiscoveryRequestRejectsWrongPrincipalAndFixedItemScopes(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	if _, _, err := fixture.controller.DiscoveryRequest(t.Context(), testPrincipal,
		"facebook", "https://www.facebook.com"); err == nil {
		t.Fatal("fixed-item scope silently widened to vault discovery")
	}
	rules := []credentialbroker.AccountRule{{
		Alias: "logins", VaultID: "vault", DiscoverLogins: true,
		Principals: []credentialbroker.Principal{testPrincipal},
	}}
	if fixture.controller.ReplacePolicy(t.Context(), rules) != nil {
		t.Fatal("discovery policy fixture failed")
	}
	other := testPrincipal
	other.ActorID = "another-actor"
	if _, _, err := fixture.controller.DiscoveryRequest(t.Context(), other,
		"logins", "https://example.com"); err == nil {
		t.Fatal("unauthenticated discovery caller was admitted")
	}
	if _, _, err := fixture.controller.DiscoveryRequest(t.Context(), testPrincipal,
		"logins", "http://example.com"); err == nil {
		t.Fatal("insecure discovery origin was admitted")
	}
}
