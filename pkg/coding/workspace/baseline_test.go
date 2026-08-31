package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
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
		ProjectKey: "project-key", Origin: BaselineOriginNew, CapturedAt: capturedAt,
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

func TestCompareBaselineClassifiesTruthfulPathTransitions(t *testing.T) {
	root := initGitRepository(t)
	tracked := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("pre-existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := NewRepository(root, root, Limits{})
	request := BaselineRequest{ProjectKey: "project-key", Origin: BaselineOriginNew, CapturedAt: time.Now().UTC()}
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
		ProjectKey: "project-key", Origin: BaselineOriginNew, CapturedAt: request.CapturedAt.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if provenanceForPath(t, CompareBaseline(baseline, resolved), "tracked.txt") !=
		ProvenanceResolvedSinceBaseline {
		t.Fatalf("resolved provenance = %#v", CompareBaseline(baseline, resolved))
	}
}

func TestCompareBaselineNeverAttributesLegacyAdoptionOrChangedAuthority(t *testing.T) {
	baseline := testBaseline(t, BaselineOriginResumeAdoption, "/repo", "head")
	current := testBaseline(t, BaselineOriginNew, "/repo", "head")
	baseline.Paths = []BaselinePath{
		{Path: "new.txt", Status: "??", Fingerprint: sha256Hex("new"), EvidenceComplete: true},
	}
	baseline.BaselineID = baselineDigest(baseline)
	current.Paths = baseline.Paths
	current.BaselineID = baselineDigest(current)
	result := CompareBaseline(baseline, current)
	if provenanceForPath(t, result, "new.txt") != ProvenanceIndeterminate || !result.Indeterminate {
		t.Fatalf("adoption provenance = %#v", result)
	}

	baseline = testBaseline(t, BaselineOriginNew, "/repo", "head")
	current = testBaseline(t, BaselineOriginNew, "/other", "head")
	result = CompareBaseline(baseline, current)
	if result.Reason != "repository authority changed since baseline" {
		t.Fatalf("authority provenance = %#v", result)
	}
}

func TestCompareBaselineTreatsAStatusTransitionAsOnePath(t *testing.T) {
	baseline := testBaseline(t, BaselineOriginNew, "/repo", "head")
	baseline.Paths = []BaselinePath{
		{Path: "changed.txt", Status: " M", Fingerprint: sha256Hex("before"), EvidenceComplete: true},
	}
	baseline.BaselineID = baselineDigest(baseline)
	current := testBaseline(t, BaselineOriginNew, "/repo", "head")
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
	baseline := testBaseline(t, BaselineOriginNew, "/repo", "head")
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

func testBaseline(t *testing.T, origin BaselineOrigin, root, head string) RepositoryBaseline {
	t.Helper()
	baseline := RepositoryBaseline{
		SchemaVersion: RepositoryBaselineSchemaV1,
		ProjectKey:    "project-key", Origin: origin, CapturedAt: time.Now().UTC(),
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
