package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepositoryBaselineCapturesBoundedFingerprintsWithoutContents(t *testing.T) {
	root := initGitRepository(t)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	capturedAt := time.Now().UTC()
	baseline, err := NewRepository(root, root, Limits{}).CaptureBaseline(t.Context(), BaselineRequest{
		ProjectKey: "project-key", CapturedAt: capturedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !baseline.RepositoryAvailable || !baseline.PathsComplete || !baseline.IndexComplete ||
		baseline.BaselineID == "" || len(baseline.Paths) != 1 || !baseline.Paths[0].EvidenceComplete ||
		baseline.Paths[0].Fingerprint == "" {
		t.Fatalf("baseline = %#v", baseline)
	}
	encoded, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("changed secret")) {
		t.Fatalf("baseline persisted file content: %s", encoded)
	}
}

func TestRepositoryBaselineOmitsUnrepresentableGitPaths(t *testing.T) {
	root := initGitRepository(t)
	path := filepath.Join(root, string([]byte{'b', 'a', 'd', 0xff}))
	if err := os.WriteFile(path, []byte("content\n"), 0o600); err != nil {
		t.Skipf("filesystem does not support non-UTF-8 paths: %v", err)
	}
	baseline, err := NewRepository(root, root, Limits{}).CaptureBaseline(t.Context(), BaselineRequest{
		ProjectKey: "project-key", CapturedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if baseline.PathsComplete || !baseline.Truncated || len(baseline.Paths) != 0 || baseline.BaselineID == "" {
		t.Fatalf("baseline = %#v", baseline)
	}
}

func TestRepositoryBaselineBoundsAggregateEncodedPaths(t *testing.T) {
	baseline := testBaseline(t, "/repo", "head")
	baseline.Paths = nil
	for index := range 128 {
		baseline.Paths = append(baseline.Paths, BaselinePath{
			Path: fmt.Sprintf("%03d-%s", index, strings.Repeat("p", 4000)), Status: "??",
			Fingerprint: sha256Hex(fmt.Sprintf("path-%d", index)), EvidenceComplete: true,
		})
	}
	boundBaselinePaths(&baseline)
	baseline.BaselineID = baselineDigest(baseline)
	if err := baseline.Validate(); err != nil {
		t.Fatal(err)
	}
	if baseline.PathsComplete || !baseline.Truncated || len(baseline.Paths) >= 128 ||
		baselineEncodedSize(baseline) > RepositoryBaselineMaxBytes {
		t.Fatalf("bounded baseline paths=%d size=%d: %#v", len(baseline.Paths), baselineEncodedSize(baseline), baseline)
	}
}

func TestCompareBaselineClassifiesTruthfulPathTransitions(t *testing.T) {
	root := initGitRepository(t)
	tracked := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("pre-existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := NewRepository(root, root, Limits{})
	request := BaselineRequest{ProjectKey: "project-key", CapturedAt: time.Now().UTC()}
	baseline, err := repository.CaptureBaseline(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := repository.Provenance(t.Context(), baseline, request.CapturedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if provenanceForPath(t, result, "tracked.txt") != ProvenancePreExisting ||
		provenanceForPath(t, result, "new.txt") != ProvenanceFirstObservedDuringThread {
		t.Fatalf("provenance = %#v", result)
	}
	if err := os.WriteFile(tracked, []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := repository.CaptureBaseline(t.Context(), BaselineRequest{
		ProjectKey: "project-key", CapturedAt: request.CapturedAt.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if provenanceForPath(t, CompareBaseline(baseline, resolved), "tracked.txt") !=
		ProvenanceResolvedSinceBaseline {
		t.Fatalf("resolved provenance = %#v", CompareBaseline(baseline, resolved))
	}
}

func TestCompareBaselineRejectsChangedAuthority(t *testing.T) {
	baseline := testBaseline(t, "/repo", "head")
	current := testBaseline(t, "/other", "head")
	result := CompareBaseline(baseline, current)
	if result.Reason != "repository authority changed since baseline" {
		t.Fatalf("authority provenance = %#v", result)
	}
}

func TestCompareBaselineTreatsAStatusTransitionAsOnePath(t *testing.T) {
	baseline := testBaseline(t, "/repo", "head")
	baseline.Paths = []BaselinePath{
		{Path: "changed.txt", Status: " M", Fingerprint: sha256Hex("before"), EvidenceComplete: true},
	}
	baseline.BaselineID = baselineDigest(baseline)
	current := testBaseline(t, "/repo", "head")
	current.Paths = []BaselinePath{
		{Path: "changed.txt", Status: "MM", Fingerprint: sha256Hex("after"), EvidenceComplete: true},
	}
	current.BaselineID = baselineDigest(current)

	result := CompareBaseline(baseline, current)
	if len(result.Paths) != 1 || result.Paths[0].Path != "changed.txt" ||
		result.Paths[0].Provenance != ProvenanceFirstObservedDuringThread {
		t.Fatalf("status transition provenance = %#v", result)
	}
}

func TestCompareBaselineDoesNotGuessAcrossStagedIdentityChanges(t *testing.T) {
	baseline := testBaseline(t, "/repo", "head")
	baseline.Paths = []BaselinePath{
		{Path: "changed.txt", Status: "M ", Fingerprint: sha256Hex("content"), EvidenceComplete: true},
	}
	baseline.BaselineID = baselineDigest(baseline)
	current := baseline
	current.IndexFingerprint = sha256Hex("different-index")
	current.CapturedAt = baseline.CapturedAt.Add(time.Second)
	current.BaselineID = baselineDigest(current)

	result := CompareBaseline(baseline, current)
	if !result.Indeterminate || result.Reason != "staged identity changed since baseline" ||
		provenanceForPath(t, result, "changed.txt") != ProvenanceIndeterminate {
		t.Fatalf("staged identity provenance = %#v", result)
	}
}

func testBaseline(t *testing.T, root, head string) RepositoryBaseline {
	t.Helper()
	baseline := RepositoryBaseline{
		SchemaVersion: RepositoryBaselineSchemaV1,
		ProjectKey:    "project-key", CapturedAt: time.Now().UTC(),
		RepositoryAvailable: true, TopLevel: root, CommonDir: root + "/.git", Head: sha256Hex(head),
		Generation: sha256Hex("generation"), PathsComplete: true, IndexComplete: true,
		IndexFingerprint: sha256Hex("index"),
		Limits: BaselineLimits{
			ChangedPaths: 128, CommandBytes: 1024, FingerprintBytes: 1024, TimeoutMillis: 1000,
		},
	}
	baseline.BaselineID = baselineDigest(baseline)
	if err := baseline.Validate(); err != nil {
		t.Fatal(err)
	}
	return baseline
}

func provenanceForPath(t *testing.T, result ProvenanceResult, path string) ProvenanceKind {
	t.Helper()
	for _, candidate := range result.Paths {
		if candidate.Path == path {
			return candidate.Provenance
		}
	}
	t.Fatalf("provenance path %q not found in %#v", path, result)
	return ""
}

func sha256Hex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
