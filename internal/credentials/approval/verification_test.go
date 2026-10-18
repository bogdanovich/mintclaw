package approval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
)

func TestPhoneVerificationPersistsOwnerWithoutAccountAccess(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.controller.BeginVerification(context.Background()); err == nil {
		t.Fatal("verification available before protected enrollment")
	}
	fixture.enroll(t)
	handler, err := NewPhoneHandler(fixture.controller)
	if err != nil {
		t.Fatal("phone handler unavailable")
	}
	begin := phoneCall(t, handler, "/api/verify/begin", map[string]any{})
	repeat := phoneCall(t, handler, "/api/verify/begin", map[string]any{})
	if begin.ID != repeat.ID || len(fixture.controller.ceremonies) != 1 {
		t.Fatal("reopening setup allocated extra challenges")
	}
	ceremony := Ceremony{ID: begin.ID, Options: begin.Options}
	body := map[string]any{"id": begin.ID, "credential": json.RawMessage(fixture.assertion(
		t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false,
	))}
	response := phoneCall(t, handler, "/api/verify/finish", body)
	if response.Status != "verified" || response.GrantID != "" || response.ExpiresAt != "" ||
		len(fixture.controller.grants) != 0 || len(fixture.controller.requests) != 0 {
		t.Fatal("passkey verification issued account access")
	}
	writer := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(writer, phoneRequestFor("/api/verify/finish", marshalFixture(t, body)))
	if writer.Code != http.StatusForbidden {
		t.Fatal("verification assertion replay accepted")
	}
	restarted, setup, err := newController(testConfig(), fixture.store)
	if err != nil || setup != nil || !restarted.ready(context.Background()) || len(restarted.grants) != 0 {
		t.Fatal("verified setup did not survive restart without grants")
	}
	_ = restarted.Close()
}

func TestPhoneVerificationCannotActivateTentativeOwner(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	restarted, setup, err := newController(testConfig(), fixture.store)
	if err != nil || setup == nil {
		t.Fatal("tentative owner did not require protected recovery")
	}
	defer func() { _ = restarted.Close() }()
	if _, err = restarted.BeginVerification(context.Background()); err == nil {
		t.Fatal("verification admitted an unaccepted reloaded enrollment")
	}
	// Even a valid assertion and retained challenge cannot bypass readiness.
	ceremony, err := restarted.begin(verify, Scope{PolicyRevision: restarted.policy.Revision()}, "")
	if err != nil {
		t.Fatal("synthetic verification setup failed")
	}
	if restarted.FinishVerification(context.Background(), ceremony.ID, fixture.assertion(
		t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false,
	)) == nil {
		t.Fatal("verification activated a tentative owner")
	}
	assertEnrollmentInactive(t, restarted)
}

func TestPhoneVerificationRejectsWrongOriginAndCeremony(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	ceremony, err := fixture.controller.BeginVerification(context.Background())
	if err != nil {
		t.Fatal("verification unavailable")
	}
	if fixture.controller.FinishVerification(context.Background(), ceremony.ID, fixture.assertion(
		t, ceremony, "https://attacker.example.org", protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false,
	)) == nil {
		t.Fatal("foreign origin verified owner")
	}
	if fixture.store.commits != 1 || len(fixture.controller.grants) != 0 {
		t.Fatal("failed verification changed durable owner or grants")
	}
	ceremony, err = fixture.controller.BeginVerification(context.Background())
	if err != nil {
		t.Fatal("fresh verification unavailable")
	}
	if _, err = fixture.controller.FinishApproval(context.Background(), ceremony.ID, fixture.assertion(
		t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false,
	)); err == nil || len(fixture.controller.grants) != 0 {
		t.Fatal("verification ceremony could approve account access")
	}
}
