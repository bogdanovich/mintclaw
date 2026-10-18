package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
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
	preparedFields                 CredentialFields
	afterPrepare                   func()
	protectOutputs                 func([]string) error
}

func (client *credentialTestClient) bindCredentialOutputs(protect func([]string) error) error {
	client.protectOutputs = protect
	return nil
}

func (client *credentialTestClient) prepareCredential(_ context.Context, fields CredentialFields) (string, error) {
	client.prepared++
	client.preparedFields = fields
	if client.afterPrepare != nil {
		client.afterPrepare()
	}
	return strings.Repeat("a", 64), nil
}

func (client *credentialTestClient) submitCredential(
	_ context.Context, _ string, username, password []byte,
) (credentialDriverResult, error) {
	client.submitted++
	if (len(username) != 0 && secretvalues.Redact(string(username)) != secretvalues.Marker) ||
		(len(password) != 0 && secretvalues.Redact(string(password)) != secretvalues.Marker) {
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
	worker := &playwrightWorker{client: client, limits: broker.config.Limits.Effective()}
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

func TestCredentialLoginPreservesNativeMainDocumentRefPrefixes(t *testing.T) {
	broker, request, client, _, lease, _ := credentialBrokerFixture(t)
	for ref, element := range broker.slots[request.SessionID].refs {
		element.Target = "f17" + element.Target
		broker.slots[request.SessionID].refs[ref] = element
	}
	result := broker.LoginCredential(t.Context(), request, lease,
		func(_ context.Context, use func([]byte, []byte) error) error {
			return use([]byte("native-ref-user"), []byte("native-ref-password"))
		})
	if result.Outcome != "logged_in" || client.submitted != 1 ||
		client.preparedFields.UsernameRef != "f17e1" || client.preparedFields.PasswordRef != "f17e2" ||
		client.preparedFields.SubmitRef != "f17e3" {
		t.Fatal("native main-document refs did not retain exact private-driver bindings")
	}
}

func TestCredentialLoginRejectsSelectedFrameBeforePrivatePreparation(t *testing.T) {
	broker, request, client, _, lease, _ := credentialBrokerFixture(t)
	session, err := broker.store.GetSession(t.Context(), request.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	catalog := testContextCatalog()
	catalog.SelectedTabID = session.TabID
	catalog.Tabs = catalog.Tabs[:1]
	catalog.Tabs[0].ID = session.TabID
	catalog.SelectedFrameID = "frame_child"
	session.ContextAuthority = &catalog
	session.FrameID = catalog.SelectedFrameID
	if err = session.Validate(); err != nil {
		t.Fatal(err)
	}
	// Seed a valid selected-frame fixture, not a context transition that would
	// itself require invalidating the already-populated snapshot authority.
	store := broker.store.(*MemoryStore)
	store.mu.Lock()
	store.sessions[session.ID] = cloneSession(session)
	store.mu.Unlock()
	retrieved := false
	result := broker.LoginCredential(t.Context(), request, lease,
		func(context.Context, func([]byte, []byte) error) error { retrieved = true; return nil })
	if result.Outcome != "denied" || retrieved || client.prepared != 0 {
		t.Fatal("selected frame reached private credential preparation or retrieval")
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

func TestCredentialRevocationDuringPreparationNeverRetrieves(t *testing.T) {
	broker, request, client, authority, lease, grantID := credentialBrokerFixture(t)
	client.afterPrepare = func() {
		if authority.Revoke(request.Principal.OwnerID, grantID) != nil {
			t.Fatal("preparation revocation fixture failed")
		}
	}
	retrieved := false
	result := broker.LoginCredential(t.Context(), request, lease,
		func(context.Context, func([]byte, []byte) error) error { retrieved = true; return nil })
	if result.Outcome != "denied" || retrieved || client.submitted != 0 || client.discarded != 1 {
		t.Fatal("revocation during preparation reached vault retrieval or retained a capsule")
	}
}

func TestCredentialSessionCanObserveAfterSnapshotInvalidationAndChecksLiveOrigin(t *testing.T) {
	for _, origin := range []string{"https://example.com", "https://other.example.org"} {
		t.Run(origin, func(t *testing.T) {
			broker, request, client, _, lease, _ := credentialBrokerFixture(t)
			result := broker.LoginCredential(t.Context(), request, lease,
				func(_ context.Context, use func([]byte, []byte) error) error {
					return use([]byte("observe-account-fixture"), []byte("observe-password-fixture"))
				})
			if result.Outcome != "logged_in" {
				t.Fatal("credential observation fixture login failed")
			}
			client.callResults["browser_snapshot"] = playwrightTextResult(
				"### Page\n- Page URL: " + origin + "/home\n" +
					"- Page Title: Fixture\n### Snapshot\n```yaml\n- heading \"Ready\"\n```",
			)
			broker.mu.Lock()
			err := broker.invalidateSnapshotLocked(t.Context(), request.SessionID, "")
			broker.mu.Unlock()
			if err != nil {
				t.Fatal("ordinary action snapshot invalidation failed")
			}
			observation, err := broker.ObserveContext(t.Context(), ObserveRequest{
				Owner: request.Owner, SessionID: request.SessionID, TabID: request.TabID,
			})
			if origin == "https://example.com" {
				if err != nil || observation.SnapshotID == "" || observation.Origin != origin {
					t.Fatal("authenticated session could not publish a fresh observation after an action")
				}
			} else if !errors.Is(err, ErrDenied) || observation.SnapshotID != "" {
				t.Fatal("replacement observation bypassed the live credential origin check")
			}
		})
	}
}

func TestCredentialSessionDeniesDownloadBeforeActionPreparation(t *testing.T) {
	broker, request, client, _, lease, _ := credentialBrokerFixture(t)
	result := broker.LoginCredential(t.Context(), request, lease,
		func(_ context.Context, use func([]byte, []byte) error) error {
			return use([]byte("download-account-fixture"), []byte("download-password-fixture"))
		})
	if result.Outcome != "logged_in" {
		t.Fatal("credential download fixture login failed")
	}
	client.callResults["browser_snapshot"] = playwrightTextResult(
		"### Page\n- Page URL: https://example.com/home\n" +
			"- Page Title: Fixture\n### Snapshot\n```yaml\n- link \"Export\" [ref=e1]\n```",
	)
	observation, err := broker.ObserveContext(t.Context(), ObserveRequest{
		Owner: request.Owner, SessionID: request.SessionID, TabID: request.TabID,
	})
	if err != nil {
		t.Fatal("download fixture observation unavailable")
	}
	_, err = broker.PrepareAction(t.Context(), PrepareActionRequest{
		Owner: request.Owner, RequestID: "request_credential_download", SessionID: request.SessionID,
		TabID: request.TabID, SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: Action{Kind: ActionDownload, Ref: stableElementRef(observation.SnapshotID, "e1")},
	})
	if !errors.Is(err, ErrDenied) {
		t.Fatal("credential session prepared an unqualified download artifact")
	}
}

func TestCredentialDiagnosticsExposeCountsWithoutPathsOrMessageHashes(t *testing.T) {
	broker, request, client, _, lease, _ := credentialBrokerFixture(t)
	result := broker.LoginCredential(t.Context(), request, lease,
		func(_ context.Context, use func([]byte, []byte) error) error {
			return use([]byte("diagnostic-account-fixture"), []byte("diagnostic-password-fixture"))
		})
	if result.Outcome != "logged_in" {
		t.Fatal("credential diagnostic fixture login failed")
	}
	payload, err := json.Marshal(map[string]any{
		"categories": []DiagnosticCategorySummary{{
			Category: DiagnosticConsoleErrors, Count: 1,
			Entries: []DiagnosticEntry{{
				Timestamp: 1, Severity: "error", Origin: "https://example.com",
				Path: "/diagnostic-password-fixture", MessageHash: strings.Repeat("a", 64),
			}},
		}},
	})
	if err != nil {
		t.Fatal("private diagnostic fixture unavailable")
	}
	client.callResults["browser_run_code_unsafe"] = playwrightTextResult(
		"### Result\nMINTCLAW_DIAGNOSTICS_RESULT_V1|ok|" + url.QueryEscape(string(payload)),
	)
	summary, err := broker.Diagnostics(t.Context(), DiagnosticsRequest{
		Owner: request.Owner, SessionID: request.SessionID, Categories: []DiagnosticCategory{DiagnosticConsoleErrors},
	})
	if err != nil || len(summary.Categories) != 1 || summary.Categories[0].Count != 1 ||
		len(summary.Categories[0].Entries) != 0 || summary.Categories[0].OmittedCount != 1 || !summary.Truncated {
		t.Fatal("credential diagnostic output retained entries or lost safe counts")
	}
	if ValidateDiagnosticSummary(summary, []DiagnosticCategory{DiagnosticConsoleErrors}) != nil {
		t.Fatal("counts-only credential diagnostics broke the result contract")
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

func TestCredentialLoginRequiresLiveRuntimeAuthorityBeforeRetrieval(t *testing.T) {
	broker, request, client, _, _, _ := credentialBrokerFixture(t)
	retrieved := false
	result := broker.LoginCredential(t.Context(), request, nil,
		func(context.Context, func([]byte, []byte) error) error { retrieved = true; return nil })
	if result.Outcome != "denied" || retrieved || client.prepared != 0 {
		t.Fatal("missing credential authority reached private vault retrieval")
	}
}
