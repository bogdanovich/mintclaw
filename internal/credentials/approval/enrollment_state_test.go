package approval

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

func assertEnrollmentInactive(t *testing.T, controller *Controller) {
	t.Helper()
	if _, err := controller.Request(
		context.Background(),
		testPrincipal,
		[]string{"facebook"},
		time.Hour,
	); !errors.Is(
		err,
		ErrUnavailable,
	) {
		t.Fatal("unaccepted owner key could request an approval")
	}
	if controller.ready(context.Background()) || !controller.owner.EnrollmentPending || len(controller.grants) != 0 {
		t.Fatal("unaccepted registration became active authority")
	}
	if controller.Check(context.Background(), "", checkedRequest(controller.policy.Revision())) == nil {
		t.Fatal("unaccepted owner admitted credential use")
	}
}

func TestRejectedEnrollmentStaysInactiveAndRecoversPrivately(t *testing.T) {
	for _, mode := range []string{"original-invite-expired", "canceled-during-write", "uncertain-write"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newFixture(t)
			enrollment, _ := NewEnrollment(fixture.setup)
			secret := bootstrap(t, enrollment)
			issued := enrollment.issued
			fixture.controller.now = func() time.Time { return issued.Add(PendingLifetime - time.Second) }
			begin, err := enrollment.begin(context.Background(), secret)
			if err != nil {
				t.Fatal("late registration begin failed")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "original-invite-expired":
				fixture.store.onCommit = func() { fixture.controller.now = func() time.Time { return issued.Add(PendingLifetime) } }
			case "canceled-during-write":
				fixture.store.onCommit = cancel
			case "uncertain-write":
				fixture.store.fail = true
			}
			if enrollment.finish(ctx, secret, begin.ID, fixture.registration(t, begin, testOrigin,
				protocol.FlagUserPresent|protocol.FlagUserVerified)) == nil {
				t.Fatal("invalidated enrollment completed")
			}
			if fixture.store.commits != 1 {
				t.Fatal("fixture did not persist a tentative registration")
			}
			if mode != "uncertain-write" {
				assertEnrollmentInactive(t, fixture.controller)
			} else if !fixture.controller.failed || fixture.controller.ready(context.Background()) {
				t.Fatal("uncertain registration remained usable")
			}
			fixture.store.fail = false
			fixture.store.onCommit = nil
			restarted, setup, err := newController(testConfig(), fixture.store)
			if err != nil || setup == nil {
				t.Fatal("tentative enrollment did not reload for protected recovery")
			}
			defer func() { _ = restarted.Close() }()
			assertEnrollmentInactive(t, restarted)
			// A cryptographically valid assertion from the rejected key cannot
			// activate it, even if an old assertion ceremony were retained.
			scope := Scope{
				Principal: testPrincipal, Accounts: []string{"facebook"},
				PolicyRevision: restarted.policy.Revision(), DurationSeconds: 3600,
			}
			requestID := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
			if !intent.ValidReference(requestID) {
				t.Fatal("synthetic request reference invalid")
			}
			restarted.requests[requestID] = &pendingRequest{scope: scope, issued: restarted.now()}
			ceremony, err := restarted.begin(approve, scope, requestID)
			if err != nil {
				t.Fatal("synthetic assertion setup failed")
			}
			if !restarted.ceremonyLive(restarted.ceremonies[ceremony.ID]) {
				t.Fatal("synthetic assertion was already invalid before readiness admission")
			}
			if _, err = restarted.FinishApproval(
				context.Background(),
				ceremony.ID,
				fixture.assertion(
					t,
					ceremony,
					testOrigin,
					protocol.FlagUserPresent|protocol.FlagUserVerified,
					1,
					false,
				),
			); err == nil {
				t.Fatal("rejected owner proof activated a grant")
			}
			assertEnrollmentInactive(t, restarted)
			// A new protected operator action can deliberately replace a pending
			// candidate. Ordinary phone links never obtain this capability.
			fresh, err := NewEnrollment(setup)
			if err != nil {
				t.Fatal("protected recovery unavailable")
			}
			secret = bootstrap(t, fresh)
			begin, err = fresh.begin(context.Background(), secret)
			if err != nil || fresh.finish(context.Background(), secret, begin.ID,
				fixture.registration(t, begin, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified)) != nil {
				t.Fatal("fresh private enrollment failed")
			}
			fixture.controller, fixture.setup = restarted, setup
			_, ceremony = fixture.approval(t, time.Hour)
			if _, err = restarted.FinishApproval(
				context.Background(),
				ceremony.ID,
				fixture.assertion(
					t,
					ceremony,
					testOrigin,
					protocol.FlagUserPresent|protocol.FlagUserVerified,
					1,
					false,
				),
			); err != nil {
				t.Fatal("accepted owner could not complete first assertion")
			}
			active, setup, err := newController(testConfig(), fixture.store)
			if err != nil || setup != nil || !active.ready(context.Background()) || len(active.grants) != 0 {
				t.Fatal("accepted activation/counter was lost or a grant survived reload")
			}
			_ = active.Close()
		})
	}
}

func TestRestartBeforeFirstAssertionRequiresProtectedSetup(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	if !fixture.controller.ready(context.Background()) {
		t.Fatal("accepted registration did not become live")
	}
	restarted, setup, err := newController(testConfig(), fixture.store)
	if err != nil || setup == nil {
		t.Fatal("interrupted initial setup did not retain protected recovery")
	}
	defer func() { _ = restarted.Close() }()
	assertEnrollmentInactive(t, restarted)
	if _, err = NewEnrollment(setup); err != nil {
		t.Fatal("interrupted setup could not use private enrollment")
	}
}
