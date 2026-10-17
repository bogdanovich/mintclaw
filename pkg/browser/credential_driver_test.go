package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bogdanovich/mintclaw/internal/secretvalues"
)

func TestPrivateCredentialResultsDoNotRevealCookiesThroughGenericOutput(t *testing.T) {
	const cookie = "private-browser-cookie-fixture-Q17"
	result := credentialDriverResult{code: "logged_in", cookies: []string{cookie}}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), cookie) || strings.Contains(fmt.Sprintf("%#v", result), cookie) {
		t.Fatal("private driver result leaked cookie data through generic output")
	}
	bound := &credentialSession{}
	defer bound.close()
	if bound.protectCookies(result.cookies) != nil {
		t.Fatal("cookie output protection unavailable")
	}
	if secretvalues.Redact(cookie) != secretvalues.Marker {
		t.Fatal("session cookie output protection was not admitted")
	}
}

func TestCredentialCookieEnvelopeRegistersBeforeReturningOrdinaryResult(t *testing.T) {
	const value = "rotated-cookie-fixture-V36"
	bound := &credentialSession{}
	defer bound.close()
	envelope, err := json.Marshal(map[string]any{
		"cookies": []string{value},
		"result":  playwrightLibraryWireResult{Content: []playwrightLibraryWireContent{{Type: "text", Text: value}}},
	})
	if err != nil {
		t.Fatal("private cookie envelope fixture unavailable")
	}
	result, err := decodeCredentialCall(playwrightTextResult(string(envelope)), nil, bound.protectCookies)
	if err != nil || result == nil || secretvalues.Redact(value) != secretvalues.Marker || len(bound.cookies) != 1 {
		t.Fatal("ordinary result was released before cookie output protection")
	}
	if bound.protectCookies([]string{value, "rotated-cookie-next-fixture-K17"}) != nil ||
		len(bound.cookies) != 2 || len(bound.release) != 2 {
		t.Fatal("cookie rotation evicted protection or duplicated registrations")
	}
	bound.close()
	if secretvalues.Redact(value) != value {
		t.Fatal("closed credential session retained a cookie output guard")
	}
	if bound.protectCookies([]string{value}) == nil {
		t.Fatal("closed credential session admitted a late cookie output")
	}
}

func TestCredentialCookieEnvelopeRejectsOpaqueAndUnprotectedOutput(t *testing.T) {
	for _, failGuard := range []bool{false, true} {
		envelope, err := json.Marshal(map[string]any{
			"cookies": []string{"opaque-cookie-fixture"},
			"result":  playwrightLibraryWireResult{Content: []playwrightLibraryWireContent{{Type: "image"}}},
		})
		if err != nil {
			t.Fatal("opaque envelope fixture unavailable")
		}
		result, err := decodeCredentialCall(playwrightTextResult(string(envelope)), nil, func([]string) error {
			if failGuard {
				return errCredentialDriver
			}
			return nil
		})
		if result != nil || !errors.Is(err, ErrWorkerLost) {
			t.Fatal("private cookie envelope released an opaque or unprotected result")
		}
	}
}

func TestCredentialCookieEnvelopeRequiresPresentNonNullInventory(t *testing.T) {
	for _, inventory := range []struct {
		name  string
		field string
		valid bool
	}{
		{name: "missing"},
		{name: "null", field: `,"cookies":null`},
		{name: "empty", field: `,"cookies":[]`, valid: true},
	} {
		t.Run(inventory.name, func(t *testing.T) {
			called := false
			envelope := `{"result":{"content":[{"type":"text","text":"fixture result"}]}` + inventory.field + `}`
			result, err := decodeCredentialCall(playwrightTextResult(envelope), nil, func(cookies []string) error {
				called = true
				if cookies == nil {
					t.Error("cookie guard received no valid inventory")
				}
				return nil
			})
			if inventory.valid {
				if err != nil || result == nil || !called {
					t.Fatal("explicit empty cookie inventory was rejected")
				}
			} else if result != nil || !errors.Is(err, ErrWorkerLost) || called {
				t.Fatal("missing or null cookie inventory released ordinary output")
			}
		})
	}
}

func TestCredentialContextCatalogGuardsPersistedURLsAndLabels(t *testing.T) {
	broker, request, _, _, _, _ := credentialBrokerFixture(t)
	const value = "catalog-credential-fixture-S47"
	release, err := secretvalues.Register([]byte(value))
	if err != nil {
		t.Fatal("catalog guard fixture unavailable")
	}
	defer release()
	catalog := testContextCatalog()
	catalog.Tabs[0].URL = "https://example.com/" + value
	catalog.Tabs[0].Title = value
	catalog.Tabs[0].Frames[0].URL = "https://example.com/frame/" + value
	catalog.Tabs[0].Frames[0].Label = value
	session, err := broker.store.GetSession(t.Context(), request.SessionID)
	if err != nil {
		t.Fatal("catalog session fixture unavailable")
	}
	broker.mu.Lock()
	public, err := broker.persistContextCatalogLocked(t.Context(), session, catalog)
	broker.mu.Unlock()
	if err != nil {
		t.Fatal("guarded catalog publication failed")
	}
	persisted, err := broker.store.GetSession(t.Context(), request.SessionID)
	encoded, encodeErr := json.Marshal([]any{public, persisted})
	if err != nil || encodeErr != nil || strings.Contains(string(encoded), value) ||
		catalog.Tabs[0].Title != value || public.Tabs[0].ID != catalog.Tabs[0].ID {
		t.Fatal("catalog persistence exposed credentials or mutated private authority")
	}
}

func TestCredentialProjectionGuardsSnapshotsLabelsAndDialogText(t *testing.T) {
	const value = "credential-observation-canary-S29"
	release, err := secretvalues.Register([]byte(value))
	if err != nil {
		t.Fatal("credential output protection unavailable")
	}
	defer release()
	private := DriverObservation{
		URL: "https://example.com/" + value, Origin: "https://example.com", Title: value,
		Snapshot:      "private echo " + value,
		Elements:      []DriverElement{{Target: "e1", Name: value}},
		PendingDialog: &DialogObservation{Message: value},
	}
	public := redactCredentialObservation(private)
	encoded, err := json.Marshal(public)
	if err != nil || strings.Contains(string(encoded), value) {
		t.Fatal("credential escaped a projected browser observation")
	}
	if private.Elements[0].Name != value || private.PendingDialog.Message != value ||
		public.Elements[0].Target != "e1" {
		t.Fatal("projection mutated private authority or public reference identity")
	}
	a, err := browserPageStateHash(private)
	if err != nil {
		t.Fatal("private page state hash unavailable")
	}
	b, err := browserPageStateHash(public)
	if err != nil || a != b {
		t.Fatal("safe projection invalidated browser document freshness")
	}
}

func TestPrivateCredentialProtocolRejectsUnexpectedResponseTypes(t *testing.T) {
	for _, result := range []*sdkmcp.CallToolResult{
		nil,
		{IsError: true, Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "private provider error"}}},
		{Content: []sdkmcp.Content{&sdkmcp.ImageContent{Data: []byte("opaque")}}},
		{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "first"}, &sdkmcp.TextContent{Text: "second"}}},
	} {
		if text, ok := credentialDriverText(result, nil); ok || text != "" {
			t.Fatal("private credential protocol admitted an untyped response")
		}
	}
}
