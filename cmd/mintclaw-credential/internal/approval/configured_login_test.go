package approval

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

func configuredLoginFixture(t *testing.T, rules []credentialbroker.AccountRule) (fixture, string) {
	t.Helper()
	fixture := newFixture(t)
	fixture.enroll(t)
	ctx := context.Background()
	if fixture.controller.ReplacePolicy(ctx, rules) != nil {
		t.Fatal("configured browser policy setup failed")
	}
	_, ceremony := fixture.approval(t, time.Hour)
	grant, err := fixture.controller.FinishApproval(ctx, ceremony.ID,
		fixture.assertion(t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false))
	if err != nil {
		t.Fatal("signed configured browser grant failed")
	}
	return fixture, grant.ID
}

func TestConfiguredLoginDerivesOnlyExplicitCanonicalEntry(t *testing.T) {
	rules := browserFlowRules()
	// A different approved origin precedes the explicit browser entry. The
	// controller must not use list ordering as a login destination choice.
	rules[0].Origins = []string{"https://static.facebook.com", "https://WWW.FACEBOOK.COM:443"}
	fixture, grantID := configuredLoginFixture(t, rules)
	ctx := context.Background()
	lease, scope, err := fixture.controller.LeaseConfiguredLogin(ctx, grantID, testPrincipal, "facebook")
	if err != nil || scope.Principal != testPrincipal || scope.Account != "facebook" ||
		scope.Origin != "https://www.facebook.com" || scope.PolicyRevision != fixture.controller.policy.Revision() {
		t.Fatal("configured browser scope was not an actual canonical policy snapshot")
	}
	flow, err := lease.Flow(ctx, scope)
	if err != nil || flow != *browserFlowRules()[0].Login {
		t.Fatal("configured entry did not carry the immutable browser flow")
	}
	if binding, bindingErr := lease.Binding(ctx, scope); bindingErr != nil ||
		binding.VaultID != "private-vault" || binding.ItemID != "private-item" {
		t.Fatal("actual signed lease did not retain its private provider binding")
	}
	mutated := scope
	mutated.Origin = "https://static.facebook.com"
	if _, flowErr := lease.Flow(ctx, mutated); flowErr == nil {
		t.Fatal("an approved non-entry origin became the configured login flow")
	}
	if _, err = lease.Flow(ctx, scope); err != nil {
		t.Fatal("mutating a returned scope changed the original private lease")
	}
}

func TestConfiguredLoginDeniesMissingFlowAndWrongAuthority(t *testing.T) {
	other := testPrincipal
	other.ActorID = "another-actor"
	rules := browserFlowRules()
	rules[0].Principals = append(rules[0].Principals, other)
	second := rules[0]
	second.Alias, second.ItemID = "craigslist", "other-private-item"
	rules = append(rules, second)
	fixture, grantID := configuredLoginFixture(t, rules)
	ctx := context.Background()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, item := range []struct {
		name      string
		ctx       context.Context
		principal credentialbroker.Principal
		account   string
		grant     string
	}{
		{"another-principal", ctx, other, "facebook", grantID},
		{"another-account", ctx, testPrincipal, "craigslist", grantID},
		{"missing-account", ctx, testPrincipal, "", grantID},
		{"malformed-grant", ctx, testPrincipal, "facebook", "caller-selected-grant"},
		{"unknown-grant", ctx, testPrincipal, "facebook", strings.Repeat("a", 64)},
		{"nil-context", nil, testPrincipal, "facebook", grantID},
		{"canceled-context", canceled, testPrincipal, "facebook", grantID},
	} {
		t.Run(item.name, func(t *testing.T) {
			lease, scope, err := fixture.controller.LeaseConfiguredLogin(
				item.ctx, item.grant, item.principal, item.account,
			)
			if err == nil || scope != (credentialbroker.Request{}) {
				t.Fatal("denied configured login returned scope authority")
			}
			select {
			case <-lease.Done():
			default:
				t.Fatal("denied configured login returned a live lease")
			}
		})
	}
	legacy, legacyGrant := configuredLoginFixture(t, testRules())
	if _, scope, err := legacy.controller.LeaseConfiguredLogin(
		ctx,
		legacyGrant,
		testPrincipal,
		"facebook",
	); err == nil ||
		scope != (credentialbroker.Request{}) {
		t.Fatal("origin-only approval was promoted into a browser flow")
	}
	var missing *Controller
	if _, scope, err := missing.LeaseConfiguredLogin(
		ctx,
		grantID,
		testPrincipal,
		"facebook",
	); err == nil ||
		scope != (credentialbroker.Request{}) {
		t.Fatal("missing controller admitted configured browser authority")
	}
}

func TestConfiguredLoginDoesNotReviveAnOldGrantAfterPolicyABA(t *testing.T) {
	fixture, grantID := configuredLoginFixture(t, browserFlowRules())
	ctx := context.Background()
	lease, original, err := fixture.controller.LeaseConfiguredLogin(ctx, grantID, testPrincipal, "facebook")
	if err != nil {
		t.Fatal("initial configured login failed")
	}
	replacement := browserFlowRules()
	replacement[0].Login.PasswordID = "new-password"
	if fixture.controller.ReplacePolicy(ctx, replacement) != nil ||
		fixture.controller.ReplacePolicy(ctx, browserFlowRules()) != nil {
		t.Fatal("configured flow policy transition failed")
	}
	if fixture.controller.policy.Revision() != original.PolicyRevision {
		t.Fatal("fixture did not exercise a restored original policy revision")
	}
	if _, scope, err := fixture.controller.LeaseConfiguredLogin(
		ctx,
		grantID,
		testPrincipal,
		"facebook",
	); err == nil ||
		scope != (credentialbroker.Request{}) {
		t.Fatal("a restored configured revision revived an old grant")
	}
	if _, err = lease.Binding(ctx, original); err == nil {
		t.Fatal("configured scope retained authority after its grant generation ended")
	}
}
