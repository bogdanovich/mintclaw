package browser

import (
	"encoding/base64"
	"testing"
)

func TestDecodePlaywrightLibraryResultBoundsContentTypes(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G'}
	result, err := decodePlaywrightLibraryResult(playwrightLibraryWireResponse{
		ID: 1,
		Result: &playwrightLibraryWireResult{Content: []playwrightLibraryWireContent{
			{Type: "text", Text: "bounded"},
			{Type: "image", MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString(png)},
		}},
	})
	if err != nil || result == nil || len(result.Content) != 2 {
		t.Fatalf("decodePlaywrightLibraryResult() = %#v, %v", result, err)
	}
	for _, wire := range []playwrightLibraryWireResponse{
		{ID: 1, Error: "private failure"},
		{ID: 1, Result: &playwrightLibraryWireResult{}},
		{ID: 1, Result: &playwrightLibraryWireResult{Content: []playwrightLibraryWireContent{{Type: "audio"}}}},
		{ID: 1, Result: &playwrightLibraryWireResult{Content: []playwrightLibraryWireContent{{
			Type: "image", MIMEType: "image/jpeg", Data: base64.StdEncoding.EncodeToString(png),
		}}}},
	} {
		if decoded, decodeErr := decodePlaywrightLibraryResult(wire); decodeErr == nil || decoded != nil {
			t.Fatalf("malformed response decoded as %#v, %v", decoded, decodeErr)
		}
	}
}

func TestPlaywrightLibraryCatalogMatchesPinnedWorkerContract(t *testing.T) {
	catalog := playwrightLibraryCatalog()
	revision, err := validatePlaywrightCatalog(catalog)
	if err != nil || revision == "" || len(catalog) != len(pinnedPlaywrightToolSchemas) {
		t.Fatalf("direct catalog revision = %q, tools = %d, error = %v", revision, len(catalog), err)
	}
}
