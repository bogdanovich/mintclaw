package approval

import (
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

func TestApprovalWaitAllowsLatePhoneApprovalWithPrivateLocatorSeparated(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	now := time.Now()
	issued := now
	fixture.controller.now = func() time.Time { return now }
	id, err := fixture.controller.Request(
		t.Context(),
		testPrincipal,
		[]string{"facebook"},
		credentialbroker.MaxGrantLifetime,
	)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := fixture.controller.WaitReference(t.Context(), testPrincipal, id)
	if err != nil || waiting.Reference == id || waiting.Deadline != issued.Add(2*time.Hour) {
		t.Fatalf("wait=%+v %v", waiting, err)
	}
	if _, err = fixture.controller.BeginApproval(t.Context(), waiting.Reference); err == nil {
		t.Fatal("durable reference opened phone ceremony")
	}
	now = now.Add(90 * time.Minute)
	if fixture.controller.ResolveWait(t.Context(), testPrincipal, waiting.Reference) != intent.ApprovalPending {
		t.Fatal("late live request expired")
	}
	wrong := testPrincipal
	wrong.ActorID = "other"
	if fixture.controller.ResolveWait(t.Context(), wrong, waiting.Reference) != intent.Denied {
		t.Fatal("foreign principal resolved wait")
	}
	ceremony, err := fixture.controller.BeginApproval(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	beforeApproval := time.Now()
	grant, err := fixture.controller.FinishApproval(t.Context(), ceremony.ID,
		fixture.assertion(t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false))
	if err != nil || grant.ExpiresAt.Sub(grant.IssuedAt) != 20*time.Hour || grant.IssuedAt.Before(beforeApproval) {
		t.Fatalf("late approval grant timing: %v", err)
	}
	for range 3 {
		if fixture.controller.ResolveWait(t.Context(), testPrincipal, waiting.Reference) != intent.GrantActive {
			t.Fatal("verified grant did not release wait")
		}
	}
	revoked, err := fixture.controller.BeginRevocation(t.Context(), grant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.controller.FinishRevocation(
		t.Context(),
		revoked.ID,
		fixture.assertion(
			t,
			revoked,
			testOrigin,
			protocol.FlagUserPresent|protocol.FlagUserVerified,
			2,
			false,
		),
	); err != nil {
		t.Fatal(err)
	}
	if fixture.controller.ResolveWait(t.Context(), testPrincipal, waiting.Reference) == intent.GrantActive {
		t.Fatal("revoked grant kept wait ready")
	}
}

func TestApprovalWaitKeepsShortCeremoniesAndOriginalTwoHourDeadline(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	now := time.Now()
	issued := now
	fixture.controller.now = func() time.Time { return now }
	id, err := fixture.controller.Request(t.Context(), testPrincipal, []string{"facebook"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	first, err := fixture.controller.BeginApproval(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(PendingLifetime)
	second, err := fixture.controller.BeginApproval(t.Context(), id)
	if err != nil || second.ID == first.ID {
		t.Fatal("expired unattempted ceremony was not replaced")
	}
	if _, err = fixture.controller.FinishApproval(
		t.Context(),
		first.ID,
		fixture.assertion(
			t,
			first,
			testOrigin,
			protocol.FlagUserPresent|protocol.FlagUserVerified,
			1,
			false,
		),
	); err == nil {
		t.Fatal("old passkey challenge remained usable")
	}
	waiting, err := fixture.controller.WaitReference(t.Context(), testPrincipal, id)
	if err != nil || waiting.Deadline != issued.Add(2*time.Hour) {
		t.Fatal("ceremony retry renewed request")
	}
	now = issued.Add(RequestLifetime - time.Second)
	last, err := fixture.controller.BeginApproval(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	now = issued.Add(RequestLifetime)
	if _, err = fixture.controller.FinishApproval(
		t.Context(),
		last.ID,
		fixture.assertion(
			t,
			last,
			testOrigin,
			protocol.FlagUserPresent|protocol.FlagUserVerified,
			1,
			false,
		),
	); err == nil {
		t.Fatal("fresh ceremony extended expired request")
	}
	if fixture.controller.ResolveWait(t.Context(), testPrincipal, waiting.Reference) != intent.Denied {
		t.Fatal("expired wait remained available")
	}
}

func TestApprovalWaitRequiresCurrentPolicyAndVerifiedMatchingGrant(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	id, err := fixture.controller.Request(t.Context(), testPrincipal, []string{"facebook"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := fixture.controller.WaitReference(t.Context(), testPrincipal, id)
	if err != nil {
		t.Fatal(err)
	}
	_, approved := fixture.approval(t, time.Hour)
	if _, err = fixture.controller.FinishApproval(
		t.Context(),
		approved.ID,
		fixture.assertion(
			t,
			approved,
			testOrigin,
			protocol.FlagUserPresent|protocol.FlagUserVerified,
			1,
			false,
		),
	); err != nil {
		t.Fatal(err)
	}
	if fixture.controller.ResolveWait(t.Context(), testPrincipal, waiting.Reference) != intent.GrantActive {
		t.Fatal("matching approved scope did not release another wait")
	}
	rules := testRules()
	rules[0].ItemID = "replacement-private-item"
	if fixture.controller.ReplacePolicy(t.Context(), rules) != nil {
		t.Fatal("policy replacement failed")
	}
	if fixture.controller.ResolveWait(t.Context(), testPrincipal, waiting.Reference) == intent.GrantActive {
		t.Fatal("stale policy kept authority")
	}
	if fixture.controller.ResolveWait(t.Context(), testPrincipal, strings.Repeat("f", 64)) != intent.Denied {
		t.Fatal("invented reference resolved")
	}
}
