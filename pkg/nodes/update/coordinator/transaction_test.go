//go:build linux || darwin

package coordinator

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	nodeupdate "github.com/bogdanovich/mintclaw/pkg/nodes/update"
)

type staticResolver struct {
	authority ResolvedRelease
	err       error
}

func (resolver staticResolver) ResolveUpdateRelease(
	context.Context,
	string,
	string,
) (ResolvedRelease, error) {
	return resolver.authority, resolver.err
}

type releaseFixture struct {
	authority      ResolvedRelease
	manifestSHA256 string
	artifactSHA256 string
	client         *http.Client
	server         *httptest.Server
	requests       atomic.Int32
	manifestData   []byte
	signatureData  []byte
	archiveData    []byte
}

func TestCoordinatorStagesAuthenticatedExactPlatformPayloadOnce(t *testing.T) {
	now := time.Date(2026, 8, 8, 7, 0, 0, 0, time.UTC)
	fixture := newReleaseFixture(t, now, "mintclaw-node")
	defer fixture.server.Close()
	coordinator, root := testCoordinator(t, fixture, now)
	defer func() { _ = coordinator.Close() }()
	request := fixture.stageRequest(now)
	state, err := coordinator.Stage(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if state.Transaction == nil || state.Transaction.Phase != PhaseStaged ||
		state.Transaction.Candidate == nil || state.Transaction.Candidate.Slot == state.Active.Slot {
		t.Fatalf("staged state = %#v", state)
	}
	candidateName, err := payloadFileName(state.Transaction.Candidate.Slot)
	if err != nil {
		t.Fatal(err)
	}
	if info, statErr := os.Stat(filepath.Join(root, candidateName)); statErr != nil || info.Mode().Perm() != 0o500 {
		t.Fatalf("candidate slot = %v, %v", info, statErr)
	}
	requests := fixture.requests.Load()
	duplicate, err := coordinator.Stage(t.Context(), request)
	if err != nil || duplicate.Generation != state.Generation || fixture.requests.Load() != requests {
		t.Fatalf(
			"duplicate Stage() = generation %d, requests %d, error %v",
			duplicate.Generation,
			fixture.requests.Load(),
			err,
		)
	}
	changed := request
	changed.ExpectedArtifactSHA256 = digestOf([]byte("changed"))
	if _, err = coordinator.Stage(t.Context(), changed); !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("changed Stage() error = %v", err)
	}
}

func TestCoordinatorReconcilesPublishedCandidateAfterStateCommitLoss(t *testing.T) {
	now := time.Date(2026, 8, 8, 7, 0, 0, 0, time.UTC)
	fixture := newReleaseFixture(t, now, "mintclaw-node")
	defer fixture.server.Close()
	coordinator, _ := testCoordinator(t, fixture, now)
	defer func() { _ = coordinator.Close() }()
	request := fixture.stageRequest(now)
	staged, err := coordinator.Stage(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	verified := staged
	verified.Generation++
	verified.Transaction.Phase = PhaseVerified
	if err = coordinator.store.Commit(staged.Generation, verified); err != nil {
		t.Fatal(err)
	}
	requests := fixture.requests.Load()
	reconciled, err := coordinator.Stage(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Transaction.Phase != PhaseStaged || reconciled.Generation != verified.Generation+1 ||
		fixture.requests.Load() != requests {
		t.Fatalf("reconciled state = %#v, requests = %d", reconciled, fixture.requests.Load())
	}
}

func TestCoordinatorFailsClosedForUntrustedManifestAndUnsafeArchive(t *testing.T) {
	now := time.Date(2026, 8, 8, 7, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name         string
		archiveEntry string
		mutate       func(*releaseFixture)
		wantCode     string
	}{
		{name: "wrong signer", archiveEntry: "mintclaw-node", wantCode: "manifest_untrusted", mutate: func(fixture *releaseFixture) {
			publicKey, _, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			fixture.authority.TrustedKey = nodeupdate.TrustedKey{
				KeyID: nodeupdate.KeyID(publicKey), PublicKey: publicKey,
			}
		}},
		{name: "archive traversal", archiveEntry: "../mintclaw-node", wantCode: "artifact_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReleaseFixture(t, now, test.archiveEntry)
			defer fixture.server.Close()
			if test.mutate != nil {
				test.mutate(fixture)
			}
			coordinator, _ := testCoordinator(t, fixture, now)
			defer func() { _ = coordinator.Close() }()
			state, err := coordinator.Stage(t.Context(), fixture.stageRequest(now))
			var stageErr *StageError
			if !errors.As(err, &stageErr) || stageErr.Code != test.wantCode {
				t.Fatalf("Stage() error = %v", err)
			}
			if state.Transaction == nil || state.Transaction.Phase != PhaseOperatorActionRequired ||
				state.Transaction.ActivationAttempted {
				t.Fatalf("terminal state = %#v", state)
			}
		})
	}
}

func TestCoordinatorRejectsPartialChangedAndStructurallyUnsafeArtifacts(t *testing.T) {
	now := time.Date(2026, 8, 8, 7, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		fixture func(*testing.T) *releaseFixture
	}{
		{name: "partial download", fixture: func(t *testing.T) *releaseFixture {
			fixture := newReleaseFixture(t, now, "mintclaw-node")
			fixture.archiveData = fixture.archiveData[:len(fixture.archiveData)-1]
			return fixture
		}},
		{name: "digest mismatch", fixture: func(t *testing.T) *releaseFixture {
			fixture := newReleaseFixture(t, now, "mintclaw-node")
			fixture.archiveData = append([]byte(nil), fixture.archiveData...)
			fixture.archiveData[len(fixture.archiveData)-1] ^= 1
			return fixture
		}},
		{name: "symlink", fixture: func(t *testing.T) *releaseFixture {
			return newReleaseFixtureWithArchive(t, now, makeTestArchive(t, []testArchiveEntry{
				{Name: "mintclaw-node", Type: tar.TypeSymlink, Linkname: "/tmp/other", Mode: 0o500},
			}))
		}},
		{name: "hardlink", fixture: func(t *testing.T) *releaseFixture {
			return newReleaseFixtureWithArchive(t, now, makeTestArchive(t, []testArchiveEntry{
				{Name: "mintclaw-node", Type: tar.TypeLink, Linkname: "other", Mode: 0o500},
			}))
		}},
		{name: "additional entry", fixture: func(t *testing.T) *releaseFixture {
			binary, err := os.ReadFile(currentTestExecutable(t))
			if err != nil {
				t.Fatal(err)
			}
			return newReleaseFixtureWithArchive(t, now, makeTestArchive(t, []testArchiveEntry{
				{Name: "mintclaw-node", Type: tar.TypeReg, Mode: 0o500, Data: binary},
				{Name: "MintClaw-node", Type: tar.TypeReg, Mode: 0o500, Data: []byte("extra")},
			}))
		}},
		{name: "non executable mode", fixture: func(t *testing.T) *releaseFixture {
			binary, err := os.ReadFile(currentTestExecutable(t))
			if err != nil {
				t.Fatal(err)
			}
			return newReleaseFixtureWithArchive(t, now, makeTestArchive(t, []testArchiveEntry{
				{Name: "mintclaw-node", Type: tar.TypeReg, Mode: 0o600, Data: binary},
			}))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := test.fixture(t)
			defer fixture.server.Close()
			coordinator, _ := testCoordinator(t, fixture, now)
			defer func() { _ = coordinator.Close() }()
			state, err := coordinator.Stage(t.Context(), fixture.stageRequest(now))
			var stageError *StageError
			if !errors.As(err, &stageError) ||
				(stageError.Code != "download_failed" && stageError.Code != "artifact_invalid") {
				t.Fatalf("Stage() error = %v", err)
			}
			if state.Transaction == nil || state.Transaction.ActivationAttempted {
				t.Fatalf("unsafe artifact state = %#v", state)
			}
		})
	}
}

func TestCoordinatorRejectsUnapprovedRedirectHost(t *testing.T) {
	now := time.Date(2026, 8, 8, 7, 0, 0, 0, time.UTC)
	fixture := newReleaseFixture(t, now, "mintclaw-node")
	fixture.server.Config.Handler = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", "https://example.com/untrusted")
		writer.WriteHeader(http.StatusFound)
	})
	defer fixture.server.Close()
	coordinator, _ := testCoordinator(t, fixture, now)
	defer func() { _ = coordinator.Close() }()
	_, err := coordinator.Stage(t.Context(), fixture.stageRequest(now))
	var stageErr *StageError
	if !errors.As(err, &stageErr) || stageErr.Code != "manifest_unavailable" {
		t.Fatalf("Stage() error = %v", err)
	}
}

func TestCoordinatorFailsClosedAndCleansTemporaryOnFullDisk(t *testing.T) {
	now := time.Date(2026, 8, 8, 7, 0, 0, 0, time.UTC)
	fixture := newReleaseFixture(t, now, "mintclaw-node")
	defer fixture.server.Close()
	coordinator, root := testCoordinator(t, fixture, now)
	defer func() { _ = coordinator.Close() }()
	coordinator.store.fault = func(point string) error {
		if point == "archive_write" {
			return unix.ENOSPC
		}
		return nil
	}
	state, err := coordinator.Stage(t.Context(), fixture.stageRequest(now))
	var stageError *StageError
	if !errors.As(err, &stageError) || stageError.Code != "download_failed" ||
		state.Transaction == nil || state.Transaction.ActivationAttempted {
		t.Fatalf("Stage() = %#v, %v", state, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".archive-") || strings.HasPrefix(entry.Name(), ".candidate-") {
			t.Fatalf("failed staging retained temporary %q", entry.Name())
		}
	}
}

func newReleaseFixture(t *testing.T, now time.Time, archiveEntry string) *releaseFixture {
	t.Helper()
	archive := makeCompanionArchive(t, archiveEntry)
	return newReleaseFixtureWithArchive(t, now, archive)
}

func newReleaseFixtureWithArchive(t *testing.T, now time.Time, archive []byte) *releaseFixture {
	t.Helper()
	artifactDigest := digestOf(archive)
	artifactName := platformArtifactName(runtime.GOOS, runtime.GOARCH)
	manifest := nodeupdate.Manifest{
		SchemaVersion: nodeupdate.ManifestSchemaV1, Release: "v1.1.0", Channel: nodeupdate.ChannelStable,
		PublishedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339),
		MinimumCoordinatorVersion: "v1.0.0", CoordinatorAPI: nodeupdate.CurrentCoordinatorAPI,
		NodeProtocol: nodeupdate.CurrentNodeProtocol, NodeConfig: nodeupdate.CurrentNodeConfig,
	}
	for _, tuple := range []struct{ platform, architecture string }{
		{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"},
	} {
		manifest.Artifacts = append(manifest.Artifacts, nodeupdate.Artifact{
			Platform: tuple.platform, Architecture: tuple.architecture,
			Name: platformArtifactName(tuple.platform, tuple.architecture),
			Size: int64(len(archive)), SHA256: artifactDigest,
		})
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifestData, signatureData, err := nodeupdate.Sign(manifest, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &releaseFixture{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fixture.requests.Add(1)
		switch filepath.Base(request.URL.Path) {
		case manifestAssetName:
			_, _ = writer.Write(fixture.manifestData)
		case signatureAssetName:
			_, _ = writer.Write(fixture.signatureData)
		case artifactName:
			_, _ = writer.Write(fixture.archiveData)
		default:
			http.NotFound(writer, request)
		}
	}))
	fixture.server = server
	fixture.client = server.Client()
	fixture.manifestData = manifestData
	fixture.signatureData = signatureData
	fixture.archiveData = archive
	fixture.manifestSHA256 = digestOf(manifestData)
	fixture.artifactSHA256 = artifactDigest
	fixture.authority = ResolvedRelease{
		Profile: "stable", ProfileRevision: "stable-v1", ReleaseAlias: "current",
		Tag: "v1.1.0", Version: "v1.1.0", BaseURL: server.URL + "/releases",
		Channel: nodeupdate.ChannelStable, AuthorityHash: digestOf([]byte("authority")),
		TrustedKey: nodeupdate.TrustedKey{KeyID: nodeupdate.KeyID(publicKey), PublicKey: publicKey},
	}
	return fixture
}

func (fixture *releaseFixture) stageRequest(now time.Time) StageRequest {
	return StageRequest{
		Identity: ExecutionIdentity{
			InvocationID: "invocation_update", ExecutionID: "execution_update",
			PlanHash: digestOf([]byte("plan")), CatalogHash: digestOf([]byte("catalog")),
			AuthorityHash: fixture.authority.AuthorityHash,
		},
		Profile: fixture.authority.Profile, ReleaseAlias: fixture.authority.ReleaseAlias,
		ExpectedManifestSHA256: fixture.manifestSHA256,
		ExpectedArtifactSHA256: fixture.artifactSHA256,
		ExpiresAt:              now.Add(10 * time.Minute),
	}
}

func testCoordinator(t *testing.T, fixture *releaseFixture, now time.Time) (*Coordinator, string) {
	t.Helper()
	stateDirectory := privateRoot(t)
	installation := testState(t).Installation
	configureRuntimeInstallation(&installation)
	installation.ConfigPath = filepath.Join(stateDirectory, "config.json")
	adoption, err := BeginAdoption(
		stateDirectory, installation, currentTestExecutable(t), "v1.0.0", "v1.0.0", os.Geteuid(), os.Getegid(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = adoption.Commit(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(stateDirectory, StoreDirectoryName)
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := New(
		store,
		staticResolver{authority: fixture.authority},
		"v1.0.0",
		WithHTTPClient(fixture.client),
		WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator, root
}

func makeCompanionArchive(t *testing.T, name string) []byte {
	t.Helper()
	binary, err := os.ReadFile(currentTestExecutable(t))
	if err != nil {
		t.Fatal(err)
	}
	return makeTestArchive(t, []testArchiveEntry{{Name: name, Type: tar.TypeReg, Mode: 0o500, Data: binary}})
}

type testArchiveEntry struct {
	Name     string
	Type     byte
	Linkname string
	Mode     int64
	Data     []byte
}

func makeTestArchive(t *testing.T, entries []testArchiveEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	compressed := gzip.NewWriter(&output)
	archive := tar.NewWriter(compressed)
	for _, entry := range entries {
		header := &tar.Header{
			Name: entry.Name, Mode: entry.Mode, Size: int64(len(entry.Data)),
			Typeflag: entry.Type, Linkname: entry.Linkname,
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(entry.Data) > 0 {
			if _, err := archive.Write(entry.Data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func platformArtifactName(platform string, architecture string) string {
	osName := map[string]string{"linux": "Linux", "darwin": "Darwin"}[platform]
	archName := map[string]string{"amd64": "x86_64", "arm64": "arm64"}[architecture]
	return "mintclaw-node_" + osName + "_" + archName + ".tar.gz"
}

func digestOf(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
