package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestExecutableBuildIDUsesArtifactContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mintclaw-fixture")
	content := []byte("fixture executable bytes\n")
	if err := os.WriteFile(path, content, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	want := executableBuildIDPrefix + hex.EncodeToString(digest[:])
	got, err := ExecutableBuildID(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || !validBuildID(got) {
		t.Fatalf("ExecutableBuildID() = %q, want %q", got, want)
	}
	if _, err := ExecutableBuildID(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("ExecutableBuildID() accepted a missing artifact")
	}
}
