package browser

import (
	"context"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/internal/secretvalues"
)

func TestCredentialGenericStepsKeepOriginalSessionGrantAndNeverRetrieveForVerification(t *testing.T) {
	broker, request, client, authority, lease, grantID := credentialBrokerFixture(t)
	request.CredentialID, request.Fields.Step = "selected-item", "credentials"
	client.result.code = "submitted"
	result := broker.LoginCredential(t.Context(), request, lease,
		func(_ context.Context, use func([]byte, []byte) error) error {
			return use([]byte("generic-account-fixture"), []byte("generic-password-fixture"))
		})
	if result.Outcome != "submitted" || result.CredentialID != "selected-item" {
		t.Fatal("generic submission claimed authentication or lost the selected record")
	}
	observeStep := func(snapshot string) {
		t.Helper()
		client.callResults["browser_snapshot"] = playwrightTextResult(
			"### Page\n- Page URL: https://example.com/next\n" +
				"- Page Title: Fixture\n### Snapshot\n```yaml\n" + snapshot + "\n```",
		)
		observation, err := broker.ObserveContext(t.Context(), ObserveRequest{
			Owner: request.Owner, SessionID: request.SessionID, TabID: request.TabID,
		})
		if err != nil {
			t.Fatal("generic continuation could not observe a protected page")
		}
		request.SnapshotID, request.SnapshotGeneration = observation.SnapshotID, observation.SnapshotGeneration
		// Only the public opaque refs from that fresh observation may be used.
		for _, line := range strings.Split(observation.Snapshot, "\n") {
			if strings.Contains(line, `"Authenticator code"`) {
				request.Fields.OTPRef = observedFixtureRef(t, line)
			}
			if strings.Contains(line, `"Verify"`) {
				request.Fields.SubmitRef = observedFixtureRef(t, line)
			}
			if strings.Contains(line, `"Log out"`) {
				request.Fields.AuthenticatedRef = observedFixtureRef(t, line)
			}
		}
	}
	observeStep(`- textbox "Authenticator code" [ref=e4]` + "\n" + `- button "Verify" [ref=e5]`)
	request.Fields.Step = "totp"
	request.CredentialID = "different-item"
	retrievedOther := false
	otherResult := broker.LoginCredential(t.Context(), request, lease,
		func(context.Context, func([]byte, []byte) error) error { retrievedOther = true; return nil })
	if otherResult.Outcome != "denied" || retrievedOther {
		t.Fatal("continuation switched Login records before private retrieval")
	}
	request.CredentialID = "selected-item"
	result = broker.LoginCredential(t.Context(), request, lease,
		func(_ context.Context, use func([]byte, []byte) error) error { return use(nil, []byte("004921")) })
	if result.Outcome != "submitted" || secretvalues.Redact("004921") != secretvalues.Marker {
		t.Fatal("code continuation failed or released its output protection")
	}
	observeStep(`- button "Log out" [ref=e6]`)
	request.Fields.Step = "verify"
	client.result.code = "logged_in"
	retrieved := false
	result = broker.LoginCredential(t.Context(), request, lease,
		func(context.Context, func([]byte, []byte) error) error { retrieved = true; return nil })
	if result.Outcome != "logged_in" || retrieved || client.submitted != 3 {
		t.Fatal("verification retrieved values or fabricated an extra submission")
	}
	broker.mu.Lock()
	bound := broker.slots[request.SessionID].credentials
	broker.mu.Unlock()
	if authority.Revoke(request.Principal.OwnerID, grantID) != nil {
		t.Fatal("generic session revocation failed")
	}
	if bound.check(t.Context(), "https://example.com") == nil {
		t.Fatal("continuation escaped the original grant's revocation")
	}
}

func observedFixtureRef(t *testing.T, line string) string {
	t.Helper()
	_, suffix, found := strings.Cut(line, "[ref=")
	ref, _, closed := strings.Cut(suffix, "]")
	if !found || !closed || ref == "" {
		t.Fatal("fresh fixture element reference unavailable")
	}
	return ref
}

func TestCredentialGenericTOTPAndVerificationRequireAnAlreadyProtectedSession(t *testing.T) {
	for _, step := range []string{"totp", "verify"} {
		broker, request, client, _, lease, _ := credentialBrokerFixture(t)
		request.CredentialID, request.Fields.Step = "item", step
		request.Fields.OTPRef, request.Fields.AuthenticatedRef = "user_ref", "user_ref"
		retrieved := false
		result := broker.LoginCredential(t.Context(), request, lease,
			func(context.Context, func([]byte, []byte) error) error { retrieved = true; return nil })
		if result.Outcome != "denied" || retrieved || client.prepared != 0 {
			t.Fatal("unbound second factor or verification acquired credential access")
		}
	}
}
