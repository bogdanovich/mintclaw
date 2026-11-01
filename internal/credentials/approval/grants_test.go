package approval

import (
	"context"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

func restartApprovalFixture(t *testing.T, fixture fixture, config Config) *Controller {
	t.Helper()
	if fixture.controller.Close() != nil {
		t.Fatal("approval shutdown failed")
	}
	controller, setup, err := newController(config, fixture.store)
	if err != nil || setup != nil {
		t.Fatal("verified owner restart failed")
	}
	t.Cleanup(func() { _ = controller.Close() })
	return controller
}

func TestGrantPersistenceDoesNotPruneTheCompletingRequest(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	id, ceremony := fixture.approval(t, time.Hour)
	now := fixture.controller.now()
	fixture.controller.requests[id].issued = now.Add(-RequestLifetime + time.Second)
	fixture.store.onCommit = func() {
		if fixture.store.commits != 2 {
			return
		}
		calls := 0
		fixture.controller.now = func() time.Time {
			calls++
			if calls > 2 {
				return now.Add(time.Second)
			}
			return now
		}
	}
	grant, err := fixture.controller.FinishApproval(t.Context(), ceremony.ID,
		fixture.assertion(t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false))
	if err != nil || grant.ID == "" || !fixture.controller.grantActive(grant.ID) {
		t.Fatal("durable commit lost the verified, bounded approval")
	}
	// Expiry of the request locator is independent of the original grant's
	// lifetime. Pruning it must happen outside the completing transaction.
	if status, _ := fixture.controller.Status(t.Context(), testPrincipal, id); status != intent.Denied {
		t.Fatal("expired request locator was renewed")
	}
}

func TestPersistedGrantRevocationAndPolicyTransitions(t *testing.T) {
	for _, mode := range []string{"revoke", "policy", "startup-policy", "equivalent-policy"} {
		t.Run(mode, func(t *testing.T) {
			fixture, id, lease := approvedLoginLease(t)
			request := checkedRequest(fixture.controller.policy.Revision())
			config := testConfig()
			switch mode {
			case "revoke":
				ceremony, err := fixture.controller.BeginRevocation(t.Context(), id)
				if err != nil || fixture.controller.FinishRevocation(t.Context(), ceremony.ID,
					fixture.assertion(t, ceremony, testOrigin,
						protocol.FlagUserPresent|protocol.FlagUserVerified, 2, false)) != nil {
					t.Fatal("verified revocation failed")
				}
			case "policy", "startup-policy":
				config.Accounts[0].ItemID = "replacement"
				if mode == "policy" && fixture.controller.ReplacePolicy(t.Context(), config.Accounts) != nil {
					t.Fatal("policy replacement failed")
				}
			case "equivalent-policy":
				if fixture.controller.ReplacePolicy(t.Context(), config.Accounts) != nil {
					t.Fatal("equivalent policy failed")
				}
			}
			restarted := restartApprovalFixture(t, fixture, config)
			select {
			case <-lease.Done():
			default:
				t.Fatal("old in-process lease survived retirement")
			}
			if mode == "equivalent-policy" {
				if restarted.Check(t.Context(), id, request) != nil {
					t.Fatal("equivalent policy lost durable approval")
				}
				return
			}
			if restarted.Check(t.Context(), id, request) == nil || len(restarted.grants) != 0 {
				t.Fatal("retired grant survived restart")
			}
			fixture.controller = restarted
			restarted = restartApprovalFixture(t, fixture, testConfig())
			if restarted.Check(t.Context(), id, request) == nil || len(restarted.grants) != 0 {
				t.Fatal("policy A-B-A across restarts revived approval")
			}
		})
	}
}

func TestPersistedGrantStoreFailureDisablesCurrentAuthority(t *testing.T) {
	for _, mode := range []string{"approve", "revoke", "policy"} {
		t.Run(mode, func(t *testing.T) {
			fixture, id, lease := approvedLoginLease(t)
			request := checkedRequest(fixture.controller.policy.Revision())
			// Fail specifically the grant snapshot, after the passkey counter was
			// persisted. An uncertain transaction cannot keep this process live.
			before := fixture.store.commits
			fixture.store.onCommit = func() {
				if mode == "policy" || fixture.store.commits == before+2 {
					fixture.store.fail = true
				}
			}
			var err error
			switch mode {
			case "approve":
				_, ceremony := fixture.approval(t, time.Hour)
				_, err = fixture.controller.FinishApproval(t.Context(), ceremony.ID,
					fixture.assertion(t, ceremony, testOrigin,
						protocol.FlagUserPresent|protocol.FlagUserVerified, 2, false))
			case "revoke":
				ceremony, beginErr := fixture.controller.BeginRevocation(t.Context(), id)
				if beginErr != nil {
					t.Fatal("revocation begin failed")
				}
				err = fixture.controller.FinishRevocation(t.Context(), ceremony.ID,
					fixture.assertion(t, ceremony, testOrigin,
						protocol.FlagUserPresent|protocol.FlagUserVerified, 2, false))
			case "policy":
				rules := testRules()
				rules[0].ItemID = "replacement"
				err = fixture.controller.ReplacePolicy(t.Context(), rules)
			}
			if err == nil || fixture.controller.Check(context.Background(), id, request) == nil {
				t.Fatal("failed durable transition left current authority usable")
			}
			select {
			case <-lease.Done():
			default:
				t.Fatal("failed durable transition left a consumer running")
			}
		})
	}
}
