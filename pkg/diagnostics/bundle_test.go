package diagnostics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/tasks"
)

func TestWriteBundleCreatesPrivateMetadataOnlyDirectoryWithVerifiedManifest(t *testing.T) {
	fixture := bundleQueryFixture(t, Selector{TaskID: "task-safe"})
	parent := t.TempDir()
	output := filepath.Join(parent, "support-bundle")
	receipt, err := WriteBundle(BundleOptions{
		OutputDir: output,
		Query:     fixture,
		Source:    DefaultBundleSource("v0.1.0-test", strings.Repeat("a", 40)),
	})
	if err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatalf("EvalSymlinks(parent): %v", err)
	}
	wantOutput := filepath.Join(resolvedParent, "support-bundle")
	if receipt.OutputDir != wantOutput {
		t.Fatalf("OutputDir = %q, want %q", receipt.OutputDir, wantOutput)
	}
	if receipt.Files != 2 || receipt.Traces != 1 || receipt.PrivacyMode != "metadata_only" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if _, err := os.Stat(filepath.Join(output, "INCOMPLETE")); !os.IsNotExist(err) {
		t.Fatalf("INCOMPLETE marker remains: %v", err)
	}

	manifestData, err := os.ReadFile(filepath.Join(output, "manifest.json"))
	if err != nil {
		t.Fatalf("ReadFile(manifest): %v", err)
	}
	if got := bytesSHA256(manifestData); got != receipt.ManifestSHA256 {
		t.Fatalf("manifest digest = %s, want %s", got, receipt.ManifestSHA256)
	}
	var manifest BundleManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("Unmarshal(manifest): %v", err)
	}
	if manifest.SchemaVersion != BundleSchemaV1 || manifest.Privacy.Private || !manifest.Privacy.UploadAllowed ||
		manifest.Privacy.AutomaticUpload || manifest.Privacy.ManifestSelfDigest != "omitted_to_avoid_self_reference" {
		t.Fatalf("manifest privacy = %#v", manifest.Privacy)
	}
	if len(manifest.Files) != 2 || len(manifest.Traces) != 1 {
		t.Fatalf("manifest files/traces = %d/%d", len(manifest.Files), len(manifest.Traces))
	}
	for _, file := range manifest.Files {
		if !filepath.IsLocal(filepath.FromSlash(file.Path)) {
			t.Fatalf("non-local manifest path = %q", file.Path)
		}
		data, readErr := os.ReadFile(filepath.Join(output, filepath.FromSlash(file.Path)))
		if readErr != nil {
			t.Fatalf("ReadFile(%s): %v", file.Path, readErr)
		}
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); got != file.SHA256 || len(data) != file.SizeBytes {
			t.Fatalf("file receipt mismatch for %s", file.Path)
		}
	}

	if runtime.GOOS != "windows" {
		if mode := fileMode(t, output).Perm(); mode != 0o700 {
			t.Fatalf("output mode = %o", mode)
		}
		for _, path := range []string{
			"manifest.json",
			"health.json",
			filepath.Join("traces", "store-safe", "trace-bundle.json"),
		} {
			if mode := fileMode(t, filepath.Join(output, path)).Perm(); mode != 0o600 {
				t.Fatalf("%s mode = %o", path, mode)
			}
		}
	}

	for _, path := range []string{
		filepath.Join(output, "manifest.json"),
		filepath.Join(output, "health.json"),
		filepath.Join(output, "traces", "store-safe", "trace-bundle.json"),
	} {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("ReadFile(%s): %v", path, readErr)
		}
		for _, forbidden := range []string{
			"private prompt",
			"private result",
			string(fixture.Matches[0].Trace.Records[0].Data),
		} {
			if forbidden != "" && strings.Contains(string(data), forbidden) {
				t.Fatalf("metadata bundle leaked %q in %s", forbidden, path)
			}
		}
	}
}

func TestWriteBundleNamespacesDuplicateTraceIDsByStore(t *testing.T) {
	created := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	stores := make([]StoreSpec, 0, 2)
	for _, storeID := range []string{"store-a", "store-b"} {
		root := filepath.Join(t.TempDir(), storeID)
		writeQueryTrace(t, root, queryTrace("trace-shared", created, "turn", ""))
		stores = append(stores, StoreSpec{ID: storeID, Kind: "profile", Root: root})
	}
	query, err := Query(QueryOptions{
		Stores: stores, Selector: Selector{TraceIDs: []string{"trace-shared"}},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	output := filepath.Join(t.TempDir(), "bundle")
	receipt, err := WriteBundle(BundleOptions{
		OutputDir: output,
		Query:     query,
		Source:    DefaultBundleSource("v0.1.0-test", ""),
	})
	if err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	if receipt.Traces != 2 || receipt.Files != 3 {
		t.Fatalf("receipt = %#v", receipt)
	}
	for _, storeID := range []string{"store-a", "store-b"} {
		path := filepath.Join(output, "traces", storeID, "trace-shared.json")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("Stat(%s): %v", path, err)
		}
	}
}

func TestWriteBundleRichModeRequiresAndLabelsOneExactTrace(t *testing.T) {
	broad := bundleQueryFixture(t, Selector{TaskID: "task-safe"})
	if _, err := WriteBundle(BundleOptions{
		OutputDir: filepath.Join(t.TempDir(), "broad"), Query: broad,
		Source: DefaultBundleSource("v0.1.0-test", ""), IncludeRedactedContent: true,
	}); err == nil || !strings.Contains(err.Error(), "one exact trace ID") {
		t.Fatalf("broad rich export error = %v", err)
	}

	exact := bundleQueryFixture(t, Selector{TraceIDs: []string{"trace-bundle"}})
	output := filepath.Join(t.TempDir(), "private")
	receipt, err := WriteBundle(BundleOptions{
		OutputDir: output, Query: exact,
		Source: DefaultBundleSource("v0.1.0-test", ""), IncludeRedactedContent: true,
	})
	if err != nil {
		t.Fatalf("WriteBundle(rich): %v", err)
	}
	if receipt.PrivacyMode != "private_redacted_content" || receipt.Files != 3 {
		t.Fatalf("receipt = %#v", receipt)
	}
	manifestData, err := os.ReadFile(filepath.Join(output, "manifest.json"))
	if err != nil {
		t.Fatalf("ReadFile(manifest): %v", err)
	}
	var manifest BundleManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("Unmarshal(manifest): %v", err)
	}
	if !manifest.Privacy.Private || manifest.Privacy.UploadAllowed || len(manifest.Privacy.Warnings) != 2 ||
		manifest.Traces[0].PrivateFile == "" {
		t.Fatalf("private manifest = %#v", manifest.Privacy)
	}
	privateData, err := os.ReadFile(filepath.Join(output, filepath.FromSlash(manifest.Traces[0].PrivateFile)))
	if err != nil {
		t.Fatalf("ReadFile(private): %v", err)
	}
	if !strings.Contains(string(privateData), "private result") {
		t.Fatalf("private trace lacks retained redacted preview: %s", privateData)
	}
}

func TestWriteBundleIncludesChecksummedOwnerSnapshot(t *testing.T) {
	fixture := bundleQueryFixture(t, Selector{TaskID: "task-safe"})
	owners := OwnerReport{
		SchemaVersion: OwnerReportSchemaV1,
		GeneratedAt:   fixture.GeneratedAt,
		Mode:          "exact_identity",
		Availability:  "available",
		Tasks: []TaskInspection{{
			StoreID: "profile-main", TaskID: "task-safe", GenerationID: "generation-safe",
			Runtime: tasks.RuntimeTool, Status: tasks.StatusSucceeded,
			DeliveryStatus: tasks.DeliveryDelivered, NotifyPolicy: tasks.NotifyDoneOnly,
		}},
	}
	output := filepath.Join(t.TempDir(), "owners")
	receipt, err := WriteBundle(BundleOptions{
		OutputDir: output, Query: fixture, Owners: &owners,
		Source: DefaultBundleSource("v0.1.0-test", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Files != 3 {
		t.Fatalf("bundle files = %d, want 3", receipt.Files)
	}
	manifestData, err := os.ReadFile(filepath.Join(output, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest BundleManifest
	if err = json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.OwnersFile != "owners.json" {
		t.Fatalf("owners file = %q", manifest.OwnersFile)
	}
	ownersData, err := os.ReadFile(filepath.Join(output, manifest.OwnersFile))
	if err != nil {
		t.Fatal(err)
	}
	var decoded OwnerReport
	if err = json.Unmarshal(ownersData, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Tasks) != 1 || decoded.Tasks[0].TaskID != "task-safe" {
		t.Fatalf("owners payload = %#v", decoded)
	}
	found := false
	for _, file := range manifest.Files {
		if file.Path == manifest.OwnersFile && file.SHA256 == bytesSHA256(ownersData) {
			found = true
		}
	}
	if !found {
		t.Fatal("manifest lacks the owners.json checksum receipt")
	}
}

func TestWriteBundleRefusesExistingOutputAndUnsafeSource(t *testing.T) {
	fixture := bundleQueryFixture(t, Selector{TraceIDs: []string{"trace-bundle"}})
	existing := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatalf("Mkdir(existing): %v", err)
	}
	if _, err := WriteBundle(BundleOptions{
		OutputDir: existing, Query: fixture, Source: DefaultBundleSource("v0.1.0-test", ""),
	}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output error = %v", err)
	}
	unsafe := filepath.Join(t.TempDir(), "unsafe")
	if _, err := WriteBundle(BundleOptions{
		OutputDir: unsafe,
		Query:     fixture,
		Source: BundleSource{
			MintClawVersion: "/private/version",
			GoVersion:       "go1.26",
			OS:              "linux",
			Architecture:    "amd64",
		},
	}); err == nil || !strings.Contains(err.Error(), "invalid MintClaw version") {
		t.Fatalf("unsafe source error = %v", err)
	}
	if _, err := os.Stat(unsafe); !os.IsNotExist(err) {
		t.Fatalf("unsafe source created output: %v", err)
	}
	unsafeStore := filepath.Join(t.TempDir(), "unsafe-store")
	fixture.Matches[0].StoreID = "../store"
	if _, err := WriteBundle(BundleOptions{
		OutputDir: unsafeStore,
		Query:     fixture,
		Source:    DefaultBundleSource("v0.1.0-test", ""),
	}); err == nil || !strings.Contains(err.Error(), "invalid trace store ID") {
		t.Fatalf("unsafe store error = %v", err)
	}
	if _, err := os.Stat(unsafeStore); !os.IsNotExist(err) {
		t.Fatalf("unsafe store created output: %v", err)
	}
}

func TestResolveNewBundleRootCanonicalizesSymlinkParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform privileges on Windows")
	}
	base := t.TempDir()
	realParent := filepath.Join(base, "real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatalf("Mkdir(real): %v", err)
	}
	linkedParent := filepath.Join(base, "linked")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	got, err := resolveNewBundleRoot(filepath.Join(linkedParent, "bundle"))
	if err != nil {
		t.Fatalf("resolveNewBundleRoot: %v", err)
	}
	canonicalParent, err := filepath.EvalSymlinks(realParent)
	if err != nil {
		t.Fatalf("EvalSymlinks(real): %v", err)
	}
	if want := filepath.Join(canonicalParent, "bundle"); got != want {
		t.Fatalf("resolveNewBundleRoot() = %q, want %q", got, want)
	}
}

func bundleQueryFixture(t *testing.T, selector Selector) QueryResult {
	t.Helper()
	root := filepath.Join(t.TempDir(), "traces")
	created := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	trace := queryTrace("trace-bundle", created, "main-turn-1", "task-safe")
	trace.Policy = diagnostictrace.CapturePolicy{
		ContentMode: diagnostictrace.ContentRedacted,
		Redactor:    "mintclaw.config_filter.v1",
	}
	trace.Source = diagnostictrace.Source{MintClawVersion: "v0.1.0-test", Commit: strings.Repeat("a", 40)}
	trace.Records[0].Data = json.RawMessage(`{
        "tool":"read_file",
        "status":"succeeded",
        "result_preview":"private result",
        "message_preview":"private prompt"
    }`)
	writeQueryTrace(t, root, trace)
	result, err := Query(QueryOptions{
		Stores: []StoreSpec{{
			ID: "store-safe", Kind: "profile", Alias: "main", Root: root,
			Capture: CaptureSettings{
				Enabled: true, ContentMode: "redacted_content", MaxTraceBytes: 2 * 1024 * 1024,
				MaxRecords: 2000, MaxRecordBytes: 16 * 1024, RetentionHours: 24, MaxTraces: 100,
			},
			Retention: 24 * time.Hour,
		}},
		Selector: selector,
		Limit:    20,
		Now:      func() time.Time { return created.Add(time.Hour) },
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	return result
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	return info.Mode()
}
