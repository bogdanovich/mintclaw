package browser

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/internal/secretvalues"
)

func TestVisibleCredentialHandoffPreservesPrivateStateAndRequiresFreshVerification(t *testing.T) {
	broker, request, client, _, lease, _ := credentialBrokerFixture(t, true)
	request.CredentialID, request.Fields.Step = "item", "credentials"
	client.result.code = "submitted"
	password := "visible-handoff-password-fixture"
	reads := 0
	consume := func(_ context.Context, use func([]byte, []byte) error) error {
		reads++
		return use([]byte("visible-handoff-user-fixture"), []byte(password))
	}
	if got := broker.LoginCredential(t.Context(), request, lease, consume); got.Outcome != "submitted" {
		t.Fatal("private fixture did not submit")
	}
	bound := broker.slots[request.SessionID].credentials
	if _, err := broker.Handoff(t.Context(), request.Owner, request.SessionID); err != nil {
		t.Fatal(err)
	}
	if got := broker.LoginCredential(t.Context(), request, lease, consume); got.Outcome != "denied" || reads != 1 {
		t.Fatal("automation retrieved values during human control")
	}
	if _, err := broker.ReleaseHandoff(t.Context(), request.Owner, request.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Resume(t.Context(), request.Owner, request.SessionID); err != nil {
		t.Fatal(err)
	}
	if got := broker.LoginCredential(t.Context(), request, lease, consume); got.Outcome != "denied" {
		t.Fatal("resumption reused stale snapshot authority")
	}
	client.callResults["browser_snapshot"] = playwrightTextResult("### Page\n" +
		"- Page URL: https://example.com/login\n- Page Title: Fixture\n### Snapshot\n```yaml\n" +
		"- textbox \"Challenge\" [ref=e4]\n- button \"Log out\" [ref=e5]\n```")
	observation, err := broker.ObserveContext(t.Context(), ObserveRequest{
		Owner: request.Owner, SessionID: request.SessionID, TabID: request.TabID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var challenge, logout string
	for _, line := range strings.Split(observation.Snapshot, "\n") {
		if strings.Contains(line, `"Challenge"`) {
			challenge = observedFixtureRef(t, line)
		}
		if strings.Contains(line, `"Log out"`) {
			logout = observedFixtureRef(t, line)
		}
	}
	if broker.slots[request.SessionID].credentials != bound || secretvalues.Redact(password) != secretvalues.Marker {
		t.Fatal("human handoff discarded credential echo protection")
	}
	if _, err = broker.CaptureScreenshot(t.Context(), ScreenshotRequest{
		Owner: request.Owner, RequestID: "private_capture", SessionID: request.SessionID,
		TabID: request.TabID, SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
	}); !errors.Is(err, ErrDenied) {
		t.Fatal("human handoff unlocked a private screenshot", err)
	}
	if _, err = broker.CreateProtectedFillBinding(t.Context(), PrepareActionRequest{
		Owner: request.Owner, RequestID: "private_input", SessionID: request.SessionID,
		TabID: request.TabID, SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: challenge},
	}); !errors.Is(err, ErrDenied) {
		t.Fatal("human handoff unlocked ordinary field input in a private login", err)
	}
	request.SnapshotID, request.SnapshotGeneration = observation.SnapshotID, observation.SnapshotGeneration
	request.Fields = CredentialFields{Step: "verify", AuthenticatedRef: logout}
	client.result.code = "logged_in"
	if got := broker.LoginCredential(t.Context(), request, lease, consume); got.Outcome != "logged_in" || reads != 1 {
		t.Fatal("fresh verification failed or retrieved credentials again")
	}
}
