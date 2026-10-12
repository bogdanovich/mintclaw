package browser

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bogdanovich/mintclaw/internal/secretvalues"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

type credentialTestClient struct {
	fakePlaywrightClient
	prepared, submitted, discarded int
	result                         credentialDriverResult
	afterPrepare                   func()
}

func (client *credentialTestClient) prepareCredential(context.Context, CredentialFields) (string, error) {
	client.prepared++
	if client.afterPrepare != nil {
		client.afterPrepare()
	}
	return strings.Repeat("a", 64), nil
}

func (client *credentialTestClient) submitCredential(
	_ context.Context, _ string, username, password []byte,
) (credentialDriverResult, error) {
	client.submitted++
	if secretvalues.Redact(string(username)) != secretvalues.Marker ||
		secretvalues.Redact(string(password)) != secretvalues.Marker {
		return credentialDriverResult{}, errCredentialDriver
	}
	return client.result, nil
}

func (client *credentialTestClient) discardCredential(context.Context, string) error {
	client.discarded++
	return nil
}

func credentialBrokerFixture(t *testing.T) (
	*Broker, CredentialLoginRequest, *credentialTestClient, *credentialbroker.Authority,
	credentialbroker.GrantLease, string,
) {
	t.Helper()
	root, _ := ephemeralPlaywrightConfig(t, false)
	store := NewMemoryStore()
	broker := newTestBroker(t, root, store, &fakeWorkerFactory{})
	owner := testOwner()
	session := testOpeningSession(owner)
	session.Profile, session.ProfileRevision = "ephemeral", "ephemeral-v1"
	session.PolicyRevision, session.DryRun = broker.policyRevision, false
	session.SnapshotID, session.SnapshotGeneration = "snapshot_fixture", 1
	session.SnapshotOrigin = "https://example.com"
	session.ExpiresAt = time.Now().Add(time.Hour).UnixNano()
	if store.CreateSession(t.Context(), session) != nil {
		t.Fatal("browser session fixture unavailable")
	}
	session.State, session.Revision = SessionReady, 2
	if store.UpdateSession(t.Context(), 1, session) != nil {
		t.Fatal("browser fixture activation failed")
	}
	client := &credentialTestClient{result: credentialDriverResult{
		code: "logged_in", cookies: []string{"fixture-private-session-cookie-M46"},
	}}
	client.callResults = map[string]*sdkmcp.CallToolResult{
		"browser_run_code_unsafe": playwrightTextResult(`### Result
"MINTCLAW_NAV_V1|ok|frame|loader|1"`),
	}
	worker := &playwrightWorker{client: client}
	navigation, err := worker.NavigationIdentity(t.Context())
	if err != nil {
		t.Fatal("browser navigation fixture unavailable")
	}
	broker.slots[session.ID] = &workerSlot{
		worker:       worker,
		navigationID: navigation,
		refs: map[string]DriverElement{
			"user_ref":   {Target: "e1"},
			"pass_ref":   {Target: "e2"},
			"submit_ref": {Target: "e3"},
		},
	}
	principal := credentialbroker.Principal{
		OwnerID: "owner", ActorID: owner.ActorID, AgentID: "browser", HostID: "gateway",
	}
	policy, err := credentialbroker.NewPolicy([]credentialbroker.AccountRule{{
		Alias: "ebay", VaultID: "vault", ItemID: "item", Origins: []string{"https://example.com"},
		Principals: []credentialbroker.Principal{principal},
	}})
	if err != nil {
		t.Fatal("credential policy fixture unavailable")
	}
	authority, err := credentialbroker.NewAuthority(policy)
	if err != nil {
		t.Fatal("credential authority fixture unavailable")
	}
	t.Cleanup(authority.Close)
	grant, err := authority.Approve(credentialbroker.Approval{
		Principal: principal, Accounts: []string{"ebay"}, PolicyRevision: policy.Revision(), Lifetime: time.Hour,
	})
	if err != nil {
		t.Fatal("credential grant fixture unavailable")
	}
	lease, err := authority.Lease(t.Context(), grant.ID, credentialbroker.Request{
		Principal: principal, Account: "ebay", Origin: "https://example.com", PolicyRevision: policy.Revision(),
	})
	if err != nil {
		t.Fatal("credential lease fixture unavailable")
	}
	request := CredentialLoginRequest{
		Owner: owner, SessionID: session.ID, TabID: session.TabID,
		SnapshotID: session.SnapshotID, SnapshotGeneration: session.SnapshotGeneration,
		Account: "ebay", Principal: principal, PolicyRevision: policy.Revision(),
		Fields: CredentialFields{UsernameRef: "user_ref", PasswordRef: "pass_ref", SubmitRef: "submit_ref"},
	}
	t.Cleanup(func() { _, _ = broker.Close(context.Background(), owner, session.ID) })
	return broker, request, client, authority, lease, grant.ID
}

func TestCredentialLoginReturnsAliasAndKeepsValuesGuardedUntilSessionClose(t *testing.T) {
	broker, request, client, _, lease, _ := credentialBrokerFixture(t)
	username, password := []byte("fixture-username-N84"), []byte("fixture-password-P93")
	result := broker.LoginCredential(t.Context(), request, lease,
		func(_ context.Context, use func([]byte, []byte) error) error { return use(username, password) })
	if result.Alias != "ebay" || result.Outcome != "logged_in" || client.submitted != 1 {
		t.Fatal("private login did not produce its fixed alias/outcome")
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), string(password)) ||
		secretvalues.Redact(string(password)) != secretvalues.Marker {
		t.Fatal("private login leaked a value or released a live session's guard")
	}
	if _, err = broker.Close(t.Context(), request.Owner, request.SessionID); err != nil {
		t.Fatal("credential session close failed")
	}
	if secretvalues.Redact(string(password)) != string(password) {
		t.Fatal("closed session retained its credential output registration")
	}
}

func TestCredentialRevocationDuringRetrievalNeverInjects(t *testing.T) {
	broker, request, client, authority, lease, grantID := credentialBrokerFixture(t)
	result := broker.LoginCredential(t.Context(), request, lease,
		func(_ context.Context, use func([]byte, []byte) error) error {
			if authority.Revoke(request.Principal.OwnerID, grantID) != nil {
				t.Fatal("credential revocation fixture failed")
			}
			return use([]byte("fixture-user"), []byte("fixture-pass"))
		})
	if result.Outcome != "denied" || client.submitted != 0 || client.discarded != 1 {
		t.Fatal("revoked credential retrieval was allowed to inject")
	}
}

func TestCredentialGrantRevocationRetiresAuthenticatedBrowserSession(t *testing.T) {
	broker, request, _, authority, lease, grantID := credentialBrokerFixture(t)
	result := broker.LoginCredential(t.Context(), request, lease,
		func(_ context.Context, use func([]byte, []byte) error) error {
			return use([]byte("retire-user-fixture"), []byte("retire-password-fixture"))
		})
	if result.Outcome != "logged_in" || authority.Revoke(request.Principal.OwnerID, grantID) != nil {
		t.Fatal("authenticated session revocation fixture failed")
	}
	deadline := time.After(2 * time.Second)
	for {
		session, err := broker.Status(t.Context(), request.Owner, request.SessionID)
		if err == nil && session.State.Terminal() {
			break
		}
		select {
		case <-deadline:
			t.Fatal("revoked credential grant retained an authenticated browser")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestCredentialLoginRejectsWrongActorBeforeVaultRetrieval(t *testing.T) {
	broker, request, client, _, lease, _ := credentialBrokerFixture(t)
	request.Principal.ActorID = "other-actor"
	retrieved := false
	result := broker.LoginCredential(t.Context(), request, lease,
		func(context.Context, func([]byte, []byte) error) error { retrieved = true; return nil })
	if result.Outcome != "denied" || retrieved || client.prepared != 0 {
		t.Fatal("another actor reached private credential retrieval")
	}
}
