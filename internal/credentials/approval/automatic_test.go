package approval

import (
	"context"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

func TestAutomaticAccessNeedsExplicitOperatorModeAndExactScope(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		config := testConfig()
		config.Automatic = automatic
		store := &memoryStore{}
		controller, _, err := newController(config, store)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = controller.Close() })
		request := checkedRequest(controller.policy.Revision())
		lease, err := controller.ActiveLogin(t.Context(), request)
		if (err == nil) != automatic {
			t.Fatal("unenrolled access did not follow explicit operator policy")
		}
		if !automatic {
			continue
		}
		if lease.Check(t.Context(), request) != nil || store.commits != 0 {
			t.Fatal("automatic admission failed or persisted runtime-only authority")
		}
		for _, field := range []string{"owner", "actor", "agent", "host", "account", "origin", "revision"} {
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
			case "account":
				other.Account = "other"
			case "origin":
				other.Origin = "https://other.example.org"
			case "revision":
				other.PolicyRevision = "other"
			}
			if _, err = controller.ActiveLogin(t.Context(), other); err == nil {
				t.Fatal("automatic access widened the configured scope")
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err = controller.ActiveLogin(ctx, request); err == nil {
			t.Fatal("canceled automatic access was admitted")
		}
		if _, err = controller.Request(
			t.Context(),
			request.Principal,
			[]string{request.Account},
			time.Hour,
		); err == nil {
			t.Fatal("automatic mode created a misleading phone consent request")
		}
		if controller.Close() != nil || lease.Check(t.Context(), request) == nil {
			t.Fatal("automatic consumer survived runtime shutdown")
		}
		select {
		case <-lease.Done():
		default:
			t.Fatal("automatic lease cancellation was not signaled")
		}
	}
}

func TestAutomaticAccessNeverBecomesPersistedPhoneApproval(t *testing.T) {
	fixture, id, oldLease := approvedLoginLease(t)
	config := testConfig()
	config.Automatic = true
	controller := restartApprovalFixture(t, fixture, config)
	request := checkedRequest(controller.policy.Revision())
	if controller.Check(t.Context(), id, request) == nil || oldLease.Check(t.Context(), request) == nil {
		t.Fatal("mode change adopted an earlier approval or lease generation")
	}
	lease, err := controller.ActiveLogin(t.Context(), request)
	if err != nil || lease.Check(t.Context(), request) != nil {
		t.Fatal("explicit automatic policy did not admit scoped access")
	}
	if controller.persistGrants() != nil {
		t.Fatal("private snapshot could not be committed")
	}
	owner, err := fixture.store.load()
	if err != nil || len(owner.Grants) != 0 {
		t.Fatal("automatic access was stored as phone approval")
	}
	fixture.controller = controller
	phone := restartApprovalFixture(t, fixture, testConfig())
	if _, err = phone.ActiveLogin(t.Context(), request); err == nil || len(phone.grants) != 0 {
		t.Fatal("automatic-to-phone mode transition bypassed human approval")
	}
	if lease.Check(t.Context(), request) == nil {
		t.Fatal("old automatic lease survived a mode transition")
	}
}

func TestAutomaticAccessReusesBoundedLeaseAndRetiresOnPolicyChange(t *testing.T) {
	config := testConfig()
	config.Automatic = true
	controller, _, err := newController(config, &memoryStore{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	request := checkedRequest(controller.policy.Revision())
	lease, err := controller.ActiveLogin(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := controller.ActiveLogin(t.Context(), request)
	if err != nil || len(controller.grants) != 1 || lease.Done() != second.Done() {
		t.Fatal("automatic access issued another grant for an active scope")
	}
	remaining, err := lease.Remaining(t.Context(), request)
	if err != nil || remaining <= 0 || remaining > credentialbroker.MaxGrantLifetime {
		t.Fatal("automatic lease lifetime was not bounded")
	}
	rules := testRules()
	rules[0].ItemID = "replacement"
	if controller.ReplacePolicy(t.Context(), rules) != nil || lease.Check(t.Context(), request) == nil {
		t.Fatal("changed policy left automatic consumers admitted")
	}
}
