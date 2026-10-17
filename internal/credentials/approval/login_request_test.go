package approval

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

func TestLoginRequestBindsCurrentPolicyWithoutIssuingAccess(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	if fixture.controller.ReplacePolicy(t.Context(), browserFlowRules()) != nil {
		t.Fatal("alias login policy fixture unavailable")
	}
	request, flow, err := fixture.controller.LoginRequest(t.Context(), testPrincipal,
		"facebook", "https://www.facebook.com")
	if err != nil || request.PolicyRevision != fixture.controller.policy.Revision() ||
		request.Principal != testPrincipal || flow.SuccessMarkerID != "authenticated" {
		t.Fatal("alias login request did not bind immutable current policy")
	}
	if _, err = fixture.controller.ActiveLogin(t.Context(), request); err == nil {
		t.Fatal("metadata admission issued credential access without a signed grant")
	}
	for _, mode := range []string{"actor", "host", "alias", "origin"} {
		principal, alias, origin := testPrincipal, "facebook", "https://www.facebook.com"
		switch mode {
		case "actor":
			principal.ActorID = "other"
		case "host":
			principal.HostID = "other"
		case "alias":
			alias = "other"
		case "origin":
			origin = "https://other.example.org"
		}
		if _, _, err = fixture.controller.LoginRequest(t.Context(), principal, alias, origin); err == nil {
			t.Fatal("alias request admitted a different caller or origin")
		}
	}
	if fixture.controller.ReplacePolicy(t.Context(), []credentialbroker.AccountRule{}) != nil {
		t.Fatal("empty policy transition failed")
	}
	if _, _, err = fixture.controller.LoginRequest(t.Context(), testPrincipal,
		"facebook", "https://www.facebook.com"); err == nil {
		t.Fatal("alias request retained obsolete policy")
	}
}
