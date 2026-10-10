package nodes

import "testing"

func TestBrowserObservationSchemaAdmitsOnlyKnownNavigationEvidence(t *testing.T) {
	schema := BrowserCommandOutputSchema(
		BrowserCommandObserve,
		[]BrowserProfileDescriptor{browserProfileDescriptorFixture()},
	)
	value := map[string]any{
		"session_id": "session_1", "tab_id": "tab_primary", "snapshot_generation": 1,
		"url": "https://example.com/", "origin": "https://example.com", "snapshot": "- paragraph Challenge",
		"elements": []any{}, "truncated": false, "http_status": 403, "anti_bot_block": "cloudflare",
	}
	if err := validateInvocationInput(schema, value); err != nil {
		t.Fatal(err)
	}
	value["anti_bot_block"] = "arbitrary_page_text"
	if err := validateInvocationInput(schema, value); err == nil {
		t.Fatal("unknown evidence admitted")
	}
	delete(value, "anti_bot_block")
	delete(value, "http_status")
	if err := validateInvocationInput(schema, value); err != nil {
		t.Fatalf("legacy observation rejected: %v", err)
	}
}
