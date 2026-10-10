package browserhost

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/browser"
)

func TestBrowserHostPreservesPrivateNavigationEvidence(t *testing.T) {
	session := &browserHostSession{tabID: "tab_primary", snapshotGeneration: 1}
	result := browserHostObservation("session_1", session, browser.DriverObservation{
		URL: "https://example.com/", Origin: "https://example.com", Title: "Just a moment",
		HTTPStatus: 403, AntiBotBlock: "cloudflare", Snapshot: "- paragraph \"Challenge\"",
	}, "document_1")
	if result.HTTPStatus != 403 || result.AntiBotBlock != "cloudflare" {
		t.Fatalf("evidence dropped: %+v", result)
	}
}
