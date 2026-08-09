package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	nodeupdate "github.com/bogdanovich/mintclaw/pkg/nodes/update"
)

func TestValidateReleaseArchiveEnforcesCoordinatorContract(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("host test executable is not an admitted node platform")
	}
	directory := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(directory, "node.tar.gz")
	writeNodeReleaseArchive(t, archivePath, executable, 0o755, false)
	if err = validateReleaseArchive(archivePath, runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatalf("validate good archive: %v", err)
	}

	writeNodeReleaseArchive(t, archivePath, executable, 0o644, false)
	if err = validateReleaseArchive(archivePath, runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("validator accepted a non-executable payload")
	}
	writeNodeReleaseArchive(t, archivePath, executable, 0o755, true)
	if err = validateReleaseArchive(archivePath, runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("validator accepted an archive with an additional entry")
	}

	wrongArchitecture := "arm64"
	if runtime.GOARCH == "arm64" {
		wrongArchitecture = "amd64"
	}
	writeNodeReleaseArchive(t, archivePath, executable, 0o755, false)
	if err = validateReleaseArchive(archivePath, runtime.GOOS, wrongArchitecture); err == nil {
		t.Fatal("validator accepted the wrong architecture")
	}
}

func writeNodeReleaseArchive(
	t *testing.T,
	path string,
	executablePath string,
	mode int64,
	extraEntry bool,
) {
	t.Helper()
	executable, err := os.Open(executablePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executable.Close() }()
	info, err := executable.Stat()
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	if err = archive.WriteHeader(
		&tar.Header{Name: "mintclaw-node", Typeflag: tar.TypeReg, Mode: mode, Size: info.Size()},
	); err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(archive, executable); err != nil {
		t.Fatal(err)
	}
	if extraEntry {
		if err = archive.WriteHeader(
			&tar.Header{Name: "README.md", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
		); err != nil {
			t.Fatal(err)
		}
		if _, err = archive.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err = compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRunSignsExactFourReleaseArtifacts(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "fixture.go")
	if err = os.WriteFile(sourcePath, []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(directory, "manifest.json")
	signaturePath := filepath.Join(directory, "manifest.sig")
	artifacts := ""
	for _, fixture := range []struct {
		platform     string
		architecture string
		name         string
	}{
		{platform: "linux", architecture: "amd64", name: "mintclaw-node_Linux_x86_64.tar.gz"},
		{platform: "linux", architecture: "arm64", name: "mintclaw-node_Linux_arm64.tar.gz"},
		{platform: "darwin", architecture: "amd64", name: "mintclaw-node_Darwin_x86_64.tar.gz"},
		{platform: "darwin", architecture: "arm64", name: "mintclaw-node_Darwin_arm64.tar.gz"},
	} {
		path := filepath.Join(directory, fixture.name)
		executablePath := buildReleaseFixture(t, directory, sourcePath, fixture.platform, fixture.architecture)
		writeNodeReleaseArchive(t, path, executablePath, 0o755, false)
		if artifacts != "" {
			artifacts += " "
		}
		artifacts += fixture.platform + "/" + fixture.architecture + "/" + path
	}
	t.Setenv(signingKeyEnvironment, base64.RawStdEncoding.EncodeToString(privateKey.Seed()))
	t.Setenv("MINTCLAW_NODE_RELEASE", "v1.2.3")
	t.Setenv("MINTCLAW_NODE_RELEASE_CHANNEL", "stable")
	t.Setenv("MINTCLAW_NODE_RELEASE_PUBLISHED_AT", "2026-08-07T19:00:00Z")
	t.Setenv("MINTCLAW_NODE_MIN_COORDINATOR_VERSION", "v1.0.0")
	t.Setenv("MINTCLAW_NODE_RELEASE_ARTIFACTS", artifacts)
	t.Setenv("MINTCLAW_NODE_MANIFEST_PATH", manifestPath)
	t.Setenv("MINTCLAW_NODE_SIGNATURE_PATH", signaturePath)
	if err = run([]string{"sign"}); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	signatureData, err := os.ReadFile(signaturePath)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	manifest, err := nodeupdate.ParseManifest(manifestData)
	if err != nil {
		t.Fatalf("ParseManifest() error = %v", err)
	}
	_, err = nodeupdate.VerifyAt(manifestData, signatureData, nodeupdate.TrustedKey{
		KeyID:     nodeupdate.KeyID(publicKey),
		PublicKey: publicKey,
	}, mustManifestTime(t, manifest.PublishedAt))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if len(manifest.Artifacts) != nodeupdate.ExpectedArtifactCount {
		t.Fatalf("manifest artifacts = %#v", manifest.Artifacts)
	}
	t.Setenv(publicKeyEnvironment, base64.RawStdEncoding.EncodeToString(publicKey))
	if err = run([]string{"verify"}); err != nil {
		t.Fatalf("verify run() error = %v", err)
	}
}

func buildReleaseFixture(
	t *testing.T,
	directory string,
	sourcePath string,
	platform string,
	architecture string,
) string {
	t.Helper()
	path := filepath.Join(directory, fmt.Sprintf("fixture-%s-%s", platform, architecture))
	command := exec.Command("go", "build", "-trimpath", "-o", path, sourcePath)
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+platform, "GOARCH="+architecture)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s/%s fixture: %v: %s", platform, architecture, err, output)
	}
	return path
}

func mustManifestTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestRunFailsClosedWithoutSigningSeed(t *testing.T) {
	t.Setenv(signingKeyEnvironment, "")
	if err := run([]string{"sign"}); err == nil {
		t.Fatal("run accepted a missing signing seed")
	}
}

func TestRunKeygenCreatesPrivateAndPublicFilesWithSafeModes(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "release.seed")
	publicPath := filepath.Join(directory, "release.pub")
	t.Setenv("MINTCLAW_NODE_PRIVATE_KEY_PATH", privatePath)
	t.Setenv("MINTCLAW_NODE_PUBLIC_KEY_PATH", publicPath)
	if err := run([]string{"keygen"}); err != nil {
		t.Fatalf("run(keygen) error = %v", err)
	}
	privateInfo, err := os.Stat(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	publicInfo, err := os.Stat(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	if privateInfo.Mode().Perm() != 0o600 || publicInfo.Mode().Perm() != 0o644 {
		t.Fatalf("key modes = %o, %o", privateInfo.Mode().Perm(), publicInfo.Mode().Perm())
	}
	if _, err = nodeupdate.ParsePublicKey(strings.TrimSpace(string(mustReadFile(t, publicPath)))); err != nil {
		t.Fatalf("public key = %v", err)
	}
	if _, err = base64.RawStdEncoding.DecodeString(
		strings.TrimSpace(string(mustReadFile(t, privatePath))),
	); err != nil {
		t.Fatalf("private seed = %v", err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
