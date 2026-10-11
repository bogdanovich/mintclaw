package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
)

func approvedLoginLease(t *testing.T) (fixture, string, LoginLease) {
	t.Helper()
	fixture := newFixture(t)
	fixture.enroll(t)
	_, ceremony := fixture.approval(t, time.Hour)
	grant, err := fixture.controller.FinishApproval(context.Background(), ceremony.ID,
		fixture.assertion(t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false))
	if err != nil {
		t.Fatal("signed grant setup failed")
	}
	lease, err := fixture.controller.LeaseLogin(context.Background(), grant.ID, checkedRequest(grant.PolicyRevision))
	if err != nil {
		t.Fatal("signed grant did not admit a private login lease")
	}
	return fixture, grant.ID, lease
}

func TestLoginLeaseSignedRetirementAndFailure(t *testing.T) {
	for _, mode := range []string{"signed-revoke", "policy-transition", "close", "uncertain-owner-write"} {
		t.Run(mode, func(t *testing.T) {
			fixture, grantID, lease := approvedLoginLease(t)
			request := checkedRequest(fixture.controller.policy.Revision())
			binding, err := lease.Binding(context.Background(), request)
			if err != nil || binding.VaultID != "private-vault" || binding.ItemID != "private-item" {
				t.Fatal("accepted binding was not privately available")
			}
			switch mode {
			case "signed-revoke", "uncertain-owner-write":
				ceremony, beginErr := fixture.controller.BeginRevocation(context.Background(), grantID)
				if beginErr != nil {
					t.Fatal("signed revocation begin failed")
				}
				fixture.store.fail = mode == "uncertain-owner-write"
				finishErr := fixture.controller.FinishRevocation(
					context.Background(),
					ceremony.ID,
					fixture.assertion(
						t,
						ceremony,
						testOrigin,
						protocol.FlagUserPresent|protocol.FlagUserVerified,
						2,
						false,
					),
				)
				if (finishErr == nil) == fixture.store.fail {
					t.Fatal("signed revocation or durable failure outcome was wrong")
				}
			case "policy-transition":
				if fixture.controller.ReplacePolicy(context.Background(), testRules()) != nil {
					t.Fatal("equivalent policy failed")
				}
				if _, err = lease.Binding(context.Background(), request); err != nil {
					t.Fatal("equivalent policy invalidated a lease")
				}
				rules := testRules()
				rules[0].ItemID = "replacement-item"
				if fixture.controller.ReplacePolicy(context.Background(), rules) != nil ||
					fixture.controller.ReplacePolicy(context.Background(), testRules()) != nil {
					t.Fatal("binding A-B-A transition failed")
				}
			case "close":
				if fixture.controller.Close() != nil {
					t.Fatal("controller shutdown failed")
				}
			}
			select {
			case <-lease.Done():
			default:
				t.Fatal("accepted retirement did not signal the isolated consumer")
			}
			if binding, err = lease.Binding(context.Background(), request); err == nil || binding.VaultID != "" {
				t.Fatal("retired lease exposed a provider binding")
			}
		})
	}
}

func TestLoginLeaseBindingScopeCancellationAndRedaction(t *testing.T) {
	fixture, grantID, lease := approvedLoginLease(t)
	request := checkedRequest(fixture.controller.policy.Revision())
	binding, err := lease.Binding(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	binding.VaultID = "caller-replacement"
	if binding, err = lease.Binding(context.Background(), request); err != nil || binding.VaultID != "private-vault" {
		t.Fatal("caller mutation changed the lease's immutable binding")
	}
	other := request
	other.Origin = "https://accounts.craigslist.org"
	if _, err = lease.Binding(context.Background(), other); err == nil {
		t.Fatal("another account's live origin admitted the binding")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = lease.Binding(ctx, request); err == nil {
		t.Fatal("canceled credential operation obtained the binding")
	}
	if _, err = lease.Binding(context.Background(), request); err != nil {
		t.Fatal("one canceled operation invalidated the owner's grant")
	}
	request.Principal.ActorID = "other-actor"
	if _, err = fixture.controller.LeaseLogin(context.Background(), grantID, request); err == nil {
		t.Fatal("another principal borrowed the grant")
	}
	if _, err = lease.Binding(context.Background(), request); err == nil {
		t.Fatal("another principal used an already borrowed login lease")
	}
	for _, value := range []any{lease, &lease, map[string]any{"lease": lease}} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		formatted := fmt.Sprintf("%#v", value)
		for _, private := range []string{grantID, "private-vault", "private-item"} {
			if strings.Contains(string(encoded), private) || strings.Contains(formatted, private) {
				t.Fatal("generic output exposed private login lease state")
			}
		}
	}
	var empty LoginLease
	select {
	case <-empty.Done():
	default:
		t.Fatal("zero login lease was not already canceled")
	}
	if _, err = empty.Binding(context.Background(), request); err == nil {
		t.Fatal("zero login lease admitted a binding")
	}
	if _, err = (*Controller)(nil).LeaseLogin(context.Background(), grantID, request); err == nil {
		t.Fatal("nil controller created a login lease")
	}
}
