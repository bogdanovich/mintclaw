package toolshared

import (
	"context"
	"testing"
)

func TestToolDocumentLocalPathsAreExactAndCopied(t *testing.T) {
	paths := []string{"relative/report.pdf", "/srv/private/Tax Form.pdf"}
	ctx := WithToolDocumentLocalPaths(context.Background(), paths)
	paths[0] = "invented.pdf"

	for _, path := range []string{"relative/report.pdf", "/srv/private/Tax Form.pdf"} {
		if !ToolDocumentLocalPathAllowed(ctx, path) {
			t.Fatalf("current-message path %q was denied", path)
		}
	}
	for _, path := range []string{"invented.pdf", "/srv/private/Other.pdf", "/srv/private/Tax  Form.pdf"} {
		if ToolDocumentLocalPathAllowed(ctx, path) {
			t.Fatalf("non-exact path %q was admitted", path)
		}
	}
	if ToolDocumentLocalPathAllowed(context.Background(), "relative/report.pdf") {
		t.Fatal("path was admitted without current-turn context")
	}
}
