package tui

import (
	"testing"

	clipboard "golang.design/x/clipboard"
)

func TestClipboardPNGFormatDoesNotRequestNativeTranscoding(t *testing.T) {
	if clipboardPNG == clipboard.FmtImage {
		t.Fatal("clipboard PNG reader uses FmtImage native transcoding")
	}
	if clipboardPNG.MIME() != "image/png" {
		t.Fatalf("clipboard PNG MIME = %q", clipboardPNG.MIME())
	}
}
