package approval

import (
	"context"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

func browserFlowRules() []credentialbroker.AccountRule {
	rules := testRules()
	rules[0].Login = &credentialbroker.LoginFlow{
		NavigationURL: "https://www.facebook.com/login", EntryOrigin: "https://www.facebook.com",
		FormID: "login", UsernameID: "username", PasswordID: "password", UsernameType: "email",
		SubmitURL: "https://www.facebook.com/session", SuccessURL: "https://www.facebook.com/home",
		SuccessMarkerID: "authenticated",
	}
	return rules
}

func TestSignedApprovalSnapshotsCanonicalFlowAndRetiresOnFlowChange(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	ctx := context.Background()
	rules := browserFlowRules()
	rules[0].Origins[0] = "https://WWW.FACEBOOK.COM:443"
	if fixture.controller.ReplacePolicy(ctx, rules) != nil {
		t.Fatal("browser flow configuration failed")
	}
	_, ceremony := fixture.approval(t, credentialbroker.MaxGrantLifetime)
	grant, err := fixture.controller.FinishApproval(ctx, ceremony.ID,
		fixture.assertion(t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false))
	if err != nil {
		t.Fatal("signed browser grant failed")
	}
	request := credentialbroker.Request{
		Principal: testPrincipal, Account: "facebook",
		Origin: "https://www.facebook.com", PolicyRevision: grant.PolicyRevision,
	}
	lease, err := fixture.controller.LeaseLogin(ctx, grant.ID, request)
	if err != nil {
		t.Fatal("browser lease failed")
	}
	origins, err := lease.Origins(ctx, request)
	if err != nil || len(origins) != 1 || origins[0] != "https://www.facebook.com" {
		t.Fatal("browser origins were not canonical")
	}
	origins[0] = "https://attacker.example"
	if copied, copyErr := lease.Origins(ctx, request); copyErr != nil || copied[0] != "https://www.facebook.com" {
		t.Fatal("caller mutated immutable origins")
	}
	if remaining, remainingErr := lease.Remaining(ctx, request); remainingErr != nil || remaining <= 0 ||
		remaining > credentialbroker.MaxGrantLifetime {
		t.Fatal("signed browser lease returned an invalid lifetime")
	}
	flow, err := lease.Flow(ctx, request)
	if err != nil || flow != *browserFlowRules()[0].Login {
		t.Fatal("canonical browser flow unavailable")
	}
	flow.PasswordID = "mutated-return"
	rules[0].Login.PasswordID = "mutated-input"
	if copied, copyErr := lease.Flow(ctx, request); copyErr != nil || copied.PasswordID != "password" {
		t.Fatal("caller mutated immutable flow")
	}
	other := request
	other.Principal.ActorID = "other"
	if _, err = lease.Flow(ctx, other); err == nil {
		t.Fatal("wrong actor reused browser flow")
	}
	if fixture.controller.ReplacePolicy(ctx, rules) != nil ||
		fixture.controller.ReplacePolicy(ctx, browserFlowRules()) != nil {
		t.Fatal("flow policy A-B-A failed")
	}
	select {
	case <-lease.Done():
	default:
		t.Fatal("flow change did not retire browser lease")
	}
	if _, err = lease.Flow(ctx, request); err == nil {
		t.Fatal("retired flow snapshot became usable")
	}
}

func TestLegacyApprovedBindingHasNoBrowserFlow(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	ctx := context.Background()
	_, ceremony := fixture.approval(t, credentialbroker.MaxGrantLifetime)
	grant, err := fixture.controller.FinishApproval(ctx, ceremony.ID,
		fixture.assertion(t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false))
	if err != nil {
		t.Fatal("signed legacy grant failed")
	}
	request := credentialbroker.Request{
		Principal: testPrincipal, Account: "facebook",
		Origin: "https://www.facebook.com", PolicyRevision: grant.PolicyRevision,
	}
	lease, err := fixture.controller.LeaseLogin(ctx, grant.ID, request)
	if err != nil {
		t.Fatal("legacy binding lease failed")
	}
	if _, err = lease.Binding(ctx, request); err != nil {
		t.Fatal("legacy private binding changed")
	}
	if _, err = lease.Flow(ctx, request); err == nil {
		t.Fatal("origin-only rule authorized browser flow")
	}
}
