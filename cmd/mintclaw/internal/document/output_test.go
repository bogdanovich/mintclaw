package document

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestParsePageSelectionRejectsAmbiguity(t *testing.T) {
	pages, err := parsePageSelection("1,3-5,8")
	if err != nil || len(pages) != 5 || pages[0] != 1 || pages[4] != 8 {
		t.Fatalf("pages = %v, err = %v", pages, err)
	}
	for _, value := range []string{"0", "2-1", "1,1", "2,1", "1,,2", "1-", "all", "-1"} {
		if pages, err = parsePageSelection(value); err == nil {
			t.Fatalf("accepted %q as %v", value, pages)
		}
	}
}

func TestPublishArtifactFileIsAtomicAndRequiresOverwrite(t *testing.T) {
	root := t.TempDir()
	ref := "document-artifact://operation/extracted-text.jsonl"
	source := filepath.Join(root, "extracted-text.jsonl")
	if err := os.WriteFile(source, []byte("new"), 0o400); err != nil {
		t.Fatal(err)
	}
	snapshot := testArtifactOpener{ref: ref, path: source}
	destination := filepath.Join(root, "result.jsonl")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishArtifactFile(snapshot, ref, destination, false); err == nil {
		t.Fatal("publication replaced an existing destination without --overwrite")
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "old" {
		t.Fatalf("failed publication changed destination: %q, %v", data, err)
	}
	if err := publishArtifactFile(snapshot, ref, destination, true); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "new" {
		t.Fatalf("published output = %q, %v", data, err)
	}
}

type testArtifactOpener struct {
	ref  string
	path string
}

func (opener testArtifactOpener) OpenArtifact(ref string) (io.ReadCloser, error) {
	if ref != opener.ref {
		return nil, errors.New("unknown artifact")
	}
	return os.Open(opener.path)
}
