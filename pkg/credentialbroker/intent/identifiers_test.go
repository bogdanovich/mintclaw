package intent

import (
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

func TestIssuedGrantReferenceFitsIntegratedApprovalBoundary(t *testing.T) {
	principal := credentialbroker.Principal{OwnerID: "owner", ActorID: "actor", AgentID: "agent", HostID: "host"}
	policy, err := credentialbroker.NewPolicy([]credentialbroker.AccountRule{{
		Alias: "facebook", VaultID: "synthetic-vault", ItemID: "synthetic-item",
		Origins: []string{"https://login.example.com"}, Principals: []credentialbroker.Principal{principal},
	}})
	if err != nil {
		t.Fatal("reference policy fixture unavailable")
	}
	authority, err := credentialbroker.NewAuthority(policy)
	if err != nil {
		t.Fatal("reference authority fixture unavailable")
	}
	t.Cleanup(authority.Close)
	grant, err := authority.Approve(credentialbroker.Approval{
		Principal: principal, Accounts: []string{"facebook"}, PolicyRevision: policy.Revision(), Lifetime: time.Hour,
	})
	if err != nil || !ValidReference(grant.ID) || ValidReference(grant.ID[:len(grant.ID)/2]) {
		t.Fatal("issued grant reference changed its approval identity or accepted truncation")
	}
}

func TestIntegratedAliasAndReferenceBounds(t *testing.T) {
	for _, value := range []string{"facebook", "ebay-prod", "A_1", strings.Repeat("a", 128)} {
		if !ValidAlias(value) {
			t.Fatal("bounded account alias was rejected")
		}
	}
	for _, value := range []string{"", "_alias", "https://example.com", "account name", "account\x00", strings.Repeat("a", 129)} {
		if ValidAlias(value) {
			t.Fatal("invalid account alias was admitted")
		}
	}
	if !ValidReference(strings.Repeat("a", 64)) {
		t.Fatal("bounded opaque reference was rejected")
	}
	for _, value := range []string{"", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64)} {
		if ValidReference(value) {
			t.Fatal("invalid opaque reference was admitted")
		}
	}
}
