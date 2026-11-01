package browser

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCredentialPreparationRecognizesOnlyExactRefreshEnvelope(t *testing.T) {
	const origin = "https://example.com"
	id := strings.Repeat("a", 64)
	for _, fixture := range []struct {
		name, text, id string
		err            error
	}{
		{"ready", `{"id":"` + id + `","origin":"` + origin + `"}`, id, nil},
		{"refresh", `{"code":"observe_required","origin":"` + origin + `"}`, "", errCredentialObserveRequired},
		{"wrong-origin", `{"code":"observe_required","origin":"https://other.example"}`, "", errCredentialDriver},
		{"missing-origin", `{"code":"observe_required"}`, "", errCredentialDriver},
		{"missing-id", `{"origin":"` + origin + `"}`, "", errCredentialDriver},
		{"unknown-code", `{"code":"private-detail-fixture","origin":"` + origin + `"}`, "", errCredentialDriver},
		{"extra-field", `{"code":"observe_required","origin":"` + origin + `","private":true}`, "", errCredentialDriver},
		{"conflicting-id", `{"id":"` + id + `","code":"observe_required","origin":"` + origin + `"}`, "", errCredentialDriver},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			got, err := decodeCredentialPreparation(fixture.text, origin)
			if got != fixture.id || !errors.Is(err, fixture.err) {
				t.Fatal("private preparation accepted an invalid or misclassified response")
			}
		})
	}
}

func TestCredentialReadinessRefreshNeverReadsValuesOrBypassesAuthority(t *testing.T) {
	for _, mode := range []string{"active", "revoked", "canceled", "transport-error"} {
		t.Run(mode, func(t *testing.T) {
			broker, request, client, authority, lease, grantID := credentialBrokerFixture(t)
			request.CredentialID, request.Fields.Step = "item", "credentials"
			client.prepareErr = errCredentialObserveRequired
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "revoked":
				client.afterPrepare = func() { _ = authority.Revoke(request.Principal.OwnerID, grantID) }
			case "canceled":
				client.afterPrepare = cancel
			case "transport-error":
				client.prepareErr = errCredentialDriver
			}
			retrieved := false
			result := broker.LoginCredential(ctx, request, lease,
				func(context.Context, func([]byte, []byte) error) error { retrieved = true; return nil })
			want := "denied"
			if mode == "active" {
				want = "observe_required"
			}
			if result.Outcome != want || retrieved || client.submitted != 0 || client.discarded != 0 ||
				broker.slots[request.SessionID].credentials != nil {
				t.Fatal("readiness refresh accessed values, fabricated a capsule, or bypassed current authority")
			}
		})
	}
}

func TestCredentialReadinessRefreshUsesNewObservationBeforeOneSubmission(t *testing.T) {
	broker, request, client, _, lease, _ := credentialBrokerFixture(t)
	request.CredentialID, request.Fields.Step = "item", "credentials"
	client.prepareErr = errCredentialObserveRequired
	retrievals := 0
	consume := func(_ context.Context, use func([]byte, []byte) error) error {
		retrievals++
		return use([]byte("refresh-account-fixture"), []byte("refresh-password-fixture"))
	}
	if got := broker.LoginCredential(t.Context(), request, lease, consume); got.Outcome != "observe_required" ||
		retrievals != 0 || client.submitted != 0 {
		t.Fatal("a stale form reached value retrieval or submission")
	}
	client.prepareErr, client.result.code = nil, "submitted"
	client.callResults["browser_snapshot"] = playwrightTextResult("### Page\n" +
		"- Page URL: https://example.com/login\n- Page Title: Fixture\n### Snapshot\n```yaml\n" +
		"- textbox \"Account\" [ref=e4]\n- textbox \"Password\" [ref=e5]\n- button \"Sign in\" [ref=e6]\n```")
	observation, err := broker.ObserveContext(t.Context(), ObserveRequest{
		Owner: request.Owner, SessionID: request.SessionID, TabID: request.TabID,
	})
	if err != nil {
		t.Fatal("fresh form observation unavailable")
	}
	request.SnapshotID, request.SnapshotGeneration = observation.SnapshotID, observation.SnapshotGeneration
	for _, line := range strings.Split(observation.Snapshot, "\n") {
		switch {
		case strings.Contains(line, `"Account"`):
			request.Fields.UsernameRef = observedFixtureRef(t, line)
		case strings.Contains(line, `"Password"`):
			request.Fields.PasswordRef = observedFixtureRef(t, line)
		case strings.Contains(line, `"Sign in"`):
			request.Fields.SubmitRef = observedFixtureRef(t, line)
		}
	}
	result := broker.LoginCredential(t.Context(), request, lease, consume)
	if result.Outcome != "submitted" || retrievals != 1 || client.submitted != 1 ||
		client.preparedFields.SubmitRef != "e6" {
		t.Fatal("fresh observation did not produce exactly one private submission on the replacement control")
	}
}
