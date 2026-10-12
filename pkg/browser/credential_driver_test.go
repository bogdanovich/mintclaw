package browser

import (
	"encoding/json"
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
	release, err := result.protectCookies()
	if err != nil {
		t.Fatal("cookie output protection unavailable")
	}
	defer release()
	if secretvalues.Redact(cookie) != secretvalues.Marker {
		t.Fatal("session cookie output protection was not admitted")
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
