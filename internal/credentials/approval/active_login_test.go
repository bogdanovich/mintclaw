package approval

import (
	"context"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
)

func TestActiveLoginReusesSignedGrantWithExactScopeAndRetiresOnClose(t *testing.T) {
	fixture, _, _ := approvedLoginLease(t)
	request := checkedRequest(fixture.controller.policy.Revision())
	_, ceremony := fixture.approval(t, 20*time.Hour)
	if _, err := fixture.controller.FinishApproval(
		t.Context(),
		ceremony.ID,
		fixture.assertion(
			t,
			ceremony,
			testOrigin,
			protocol.FlagUserPresent|protocol.FlagUserVerified,
			2,
			false,
		),
	); err != nil {
		t.Fatal("longer signed grant fixture unavailable")
	}
	for range 2 {
		lease, err := fixture.controller.ActiveLogin(t.Context(), request)
		binding, bindErr := lease.Binding(t.Context(), request)
		if err != nil || bindErr != nil || lease.Check(t.Context(), request) != nil ||
			binding.VaultID != "private-vault" || binding.ItemID != "private-item" {
			t.Fatal("signed grant could not be reused with its exact private binding")
		}
		if remaining, remainErr := lease.Remaining(t.Context(), request); remainErr != nil || remaining < 19*time.Hour {
			t.Fatal("active grant lookup selected an earlier expiring authorization")
		}
	}
	for _, field := range []string{"owner", "actor", "agent", "host", "alias", "origin", "revision"} {
		other := request
		switch field {
		case "owner":
			other.Principal.OwnerID = "other"
		case "actor":
			other.Principal.ActorID = "other"
		case "agent":
			other.Principal.AgentID = "other"
		case "host":
			other.Principal.HostID = "other"
		case "alias":
			other.Account = "other"
		case "origin":
			other.Origin = "https://other.example.org"
		case "revision":
			other.PolicyRevision = "other"
		}
		if _, err := fixture.controller.ActiveLogin(context.Background(), other); err == nil {
			t.Fatal("signed grant lookup accepted a different authenticated scope")
		}
	}
	if fixture.controller.Close() != nil {
		t.Fatal("approval fixture close failed")
	}
	if _, err := fixture.controller.ActiveLogin(t.Context(), request); err == nil {
		t.Fatal("closed controller reused a credential grant")
	}
}
