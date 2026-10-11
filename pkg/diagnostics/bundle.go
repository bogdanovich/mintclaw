package diagnostics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	BundleSchemaV1 = "mintclaw.diagnostic_support_bundle.v1"
	healthSchemaV1 = "mintclaw.diagnostic_support_health.v1"
)

var defaultBundleOmissions = []string{
	"absolute_paths",
	"channel_destinations",
	"commands_and_output",
	"credentials_and_environment",
	"prompts_responses_and_rich_previews",
	"service_logs_not_requested",
	"source_and_diff_content",
	"tool_arguments_and_results",
	"unrelated_traces",
}

type BundleSource struct {
	MintClawVersion string `json:"mintclaw_version,omitempty"`
	Commit          string `json:"commit,omitempty"`
	GoVersion       string `json:"go_version"`
	OS              string `json:"os"`
	Architecture    string `json:"architecture"`
}

type BundleOptions struct {
	OutputDir              string
	Query                  QueryResult
	Source                 BundleSource
	IncludeRedactedContent bool
}

type BundleFile struct {
	Path      string `json:"path"`
	SizeBytes int    `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	Mode      string `json:"mode"`
}

type BundleTrace struct {
	StoreID      string                         `json:"store_id"`
	TraceID      string                         `json:"trace_id"`
	CreatedAt    time.Time                      `json:"created_at"`
	Source       diagnostictrace.Source         `json:"source"`
	Policy       diagnostictrace.CapturePolicy  `json:"policy"`
	Limits       diagnostictrace.AppliedLimits  `json:"limits"`
	Redaction    string                         `json:"redaction,omitempty"`
	Outcome      *diagnostictrace.Outcome       `json:"outcome,omitempty"`
	Truncation   diagnostictrace.Truncation     `json:"truncation,omitempty"`
	Projection   diagnostictrace.SafeProjection `json:"projection"`
	RecordCount  int                            `json:"record_count"`
	MetadataFile string                         `json:"metadata_file"`
	PrivateFile  string                         `json:"private_file,omitempty"`
}

type BundlePrivacy struct {
	Mode               string   `json:"mode"`
	Private            bool     `json:"private"`
	UploadAllowed      bool     `json:"upload_allowed"`
	AutomaticUpload    bool     `json:"automatic_upload"`
	Omissions          []string `json:"omissions"`
	Warnings           []string `json:"warnings,omitempty"`
	ManifestSelfDigest string   `json:"manifest_self_digest"`
}

type BundleManifest struct {
	SchemaVersion  string        `json:"schema_version"`
	CreatedAt      time.Time     `json:"created_at"`
	Source         BundleSource  `json:"source"`
	Privacy        BundlePrivacy `json:"privacy"`
	Selector       Selector      `json:"selector"`
	Stores         []StoreHealth `json:"stores"`
	Problems       []Problem     `json:"problems,omitempty"`
	Traces         []BundleTrace `json:"traces"`
	Files          []BundleFile  `json:"files"`
	MatchTruncated bool          `json:"match_truncated,omitempty"`
	ScanTruncated  bool          `json:"scan_truncated,omitempty"`
}

type BundleReceipt struct {
	OutputDir      string `json:"output_dir"`
	ManifestPath   string `json:"manifest_path"`
	ManifestSHA256 string `json:"manifest_sha256"`
	Files          int    `json:"files"`
	Traces         int    `json:"traces"`
	PrivacyMode    string `json:"privacy_mode"`
}

type healthReport struct {
	SchemaVersion  string        `json:"schema_version"`
	GeneratedAt    time.Time     `json:"generated_at"`
	Stores         []StoreHealth `json:"stores"`
	Problems       []Problem     `json:"problems,omitempty"`
	TotalMatches   int           `json:"total_matches"`
	MatchTruncated bool          `json:"match_truncated,omitempty"`
	ScanTruncated  bool          `json:"scan_truncated,omitempty"`
}

// WriteBundle writes one new support directory. The output must not already
// exist. The manifest is published last and the temporary INCOMPLETE marker is
// removed only after all payloads are durable.
func WriteBundle(options BundleOptions) (BundleReceipt, error) {
	if len(options.Query.Matches) == 0 {
		return BundleReceipt{}, fmt.Errorf("diagnostic support bundle: no matching traces")
	}
	if options.Query.MatchTruncated {
		return BundleReceipt{}, fmt.Errorf("diagnostic support bundle: matching trace set is truncated")
	}
	if options.IncludeRedactedContent {
		if len(options.Query.Selector.TraceIDs) != 1 || len(options.Query.Matches) != 1 {
			return BundleReceipt{}, fmt.Errorf(
				"diagnostic support bundle: private rich export requires one exact trace ID",
			)
		}
	}
	if err := validateBundleSource(options.Source); err != nil {
		return BundleReceipt{}, err
	}
	output, resolveErr := resolveNewBundleRoot(options.OutputDir)
	if resolveErr != nil {
		return BundleReceipt{}, resolveErr
	}
	if mkdirErr := os.Mkdir(output, 0o700); mkdirErr != nil {
		return BundleReceipt{}, fmt.Errorf("diagnostic support bundle: create output: %w", mkdirErr)
	}
	if chmodErr := os.Chmod(output, 0o700); chmodErr != nil {
		return BundleReceipt{}, fmt.Errorf("diagnostic support bundle: secure output: %w", chmodErr)
	}
	if syncParentErr := fileutil.SyncDirectory(filepath.Dir(output)); syncParentErr != nil {
		return BundleReceipt{}, fmt.Errorf("diagnostic support bundle: sync output parent: %w", syncParentErr)
	}
	incompletePath := filepath.Join(output, "INCOMPLETE")
	if markerErr := writeBundleFile(incompletePath, []byte("bundle publication incomplete\n")); markerErr != nil {
		return BundleReceipt{}, markerErr
	}

	files := make([]BundleFile, 0, len(options.Query.Matches)+2)
	health := healthReport{
		SchemaVersion: healthSchemaV1, GeneratedAt: options.Query.GeneratedAt,
		Stores: options.Query.Stores, Problems: options.Query.Problems,
		TotalMatches: options.Query.TotalMatches, MatchTruncated: options.Query.MatchTruncated,
		ScanTruncated: options.Query.ScanTruncated,
	}
	healthData, marshalHealthErr := marshalBundleJSON(health)
	if marshalHealthErr != nil {
		return BundleReceipt{}, marshalHealthErr
	}
	if writeHealthErr := writePayload(output, "health.json", healthData, &files); writeHealthErr != nil {
		return BundleReceipt{}, writeHealthErr
	}

	traces := make([]BundleTrace, 0, len(options.Query.Matches))
	for _, match := range options.Query.Matches {
		summary, summaryErr := diagnostictrace.Summarize(match.Trace)
		if summaryErr != nil {
			return BundleReceipt{}, summaryErr
		}
		relative := filepath.ToSlash(filepath.Join("traces", summary.TraceID+".json"))
		data, marshalErr := marshalBundleJSON(summary)
		if marshalErr != nil {
			return BundleReceipt{}, marshalErr
		}
		if writeTraceErr := writePayload(output, relative, data, &files); writeTraceErr != nil {
			return BundleReceipt{}, writeTraceErr
		}
		entry := BundleTrace{
			StoreID: match.StoreID, TraceID: summary.TraceID, CreatedAt: summary.CreatedAt,
			Source: summary.Source, Policy: summary.Policy, Limits: summary.Limits,
			Redaction: summary.Policy.Redactor, Outcome: summary.Outcome, Truncation: summary.Truncation,
			Projection: summary.Projection, RecordCount: len(summary.Records), MetadataFile: relative,
		}
		if options.IncludeRedactedContent {
			privateRelative := filepath.ToSlash(filepath.Join("private", "traces", summary.TraceID+".json"))
			privateData, marshalErr := marshalBundleJSON(match.Trace)
			if marshalErr != nil {
				return BundleReceipt{}, marshalErr
			}
			if writePrivateErr := writePayload(output, privateRelative, privateData, &files); writePrivateErr != nil {
				return BundleReceipt{}, writePrivateErr
			}
			entry.PrivateFile = privateRelative
		}
		traces = append(traces, entry)
	}
	slices.SortFunc(traces, func(a, b BundleTrace) int { return strings.Compare(a.TraceID, b.TraceID) })
	slices.SortFunc(files, func(a, b BundleFile) int { return strings.Compare(a.Path, b.Path) })

	privacyMode := "metadata_only"
	omissions := append([]string(nil), defaultBundleOmissions...)
	private := false
	warnings := []string(nil)
	if options.IncludeRedactedContent {
		privacyMode = "private_redacted_content"
		private = true
		omissions = []string{"service_logs_not_requested", "unrelated_traces"}
		warnings = []string{
			"contains_private_redacted_trace_content",
			"do_not_upload_without_explicit_operator_review",
		}
	}
	manifest := BundleManifest{
		SchemaVersion: BundleSchemaV1, CreatedAt: options.Query.GeneratedAt, Source: options.Source,
		Privacy: BundlePrivacy{
			Mode: privacyMode, Private: private, UploadAllowed: !private, AutomaticUpload: false,
			Omissions: omissions, Warnings: warnings, ManifestSelfDigest: "omitted_to_avoid_self_reference",
		},
		Selector: options.Query.Selector, Stores: options.Query.Stores, Problems: options.Query.Problems,
		Traces: traces, Files: files, MatchTruncated: options.Query.MatchTruncated,
		ScanTruncated: options.Query.ScanTruncated,
	}
	manifestData, marshalManifestErr := marshalBundleJSON(manifest)
	if marshalManifestErr != nil {
		return BundleReceipt{}, marshalManifestErr
	}
	manifestPath := filepath.Join(output, "manifest.json")
	if writeManifestErr := writeBundleFile(manifestPath, manifestData); writeManifestErr != nil {
		return BundleReceipt{}, writeManifestErr
	}
	if syncErr := fileutil.SyncDirectory(output); syncErr != nil {
		return BundleReceipt{}, fmt.Errorf("diagnostic support bundle: sync output: %w", syncErr)
	}
	if removeErr := os.Remove(incompletePath); removeErr != nil {
		return BundleReceipt{}, fmt.Errorf("diagnostic support bundle: finalize output: %w", removeErr)
	}
	if syncErr := fileutil.SyncDirectory(output); syncErr != nil {
		return BundleReceipt{}, fmt.Errorf("diagnostic support bundle: sync final output: %w", syncErr)
	}
	return BundleReceipt{
		OutputDir: output, ManifestPath: manifestPath, ManifestSHA256: bytesSHA256(manifestData),
		Files: len(files), Traces: len(traces), PrivacyMode: privacyMode,
	}, nil
}

func DefaultBundleSource(version, commit string) BundleSource {
	return BundleSource{
		MintClawVersion: version,
		Commit:          commit,
		GoVersion:       runtime.Version(),
		OS:              runtime.GOOS,
		Architecture:    runtime.GOARCH,
	}
}

func validateBundleSource(source BundleSource) error {
	values := []struct {
		name  string
		value string
	}{
		{"MintClaw version", source.MintClawVersion},
		{"Go version", source.GoVersion},
		{"operating system", source.OS},
		{"architecture", source.Architecture},
	}
	for _, candidate := range values {
		if candidate.value == "" || !safeSelectorPattern.MatchString(candidate.value) {
			return fmt.Errorf("diagnostic support bundle: invalid %s", candidate.name)
		}
	}
	if source.Commit != "" {
		if len(source.Commit) < 7 || len(source.Commit) > 64 || source.Commit != strings.ToLower(source.Commit) {
			return fmt.Errorf("diagnostic support bundle: invalid source commit")
		}
		if _, err := hex.DecodeString(source.Commit); err != nil {
			return fmt.Errorf("diagnostic support bundle: invalid source commit")
		}
	}
	return nil
}

func resolveNewBundleRoot(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("diagnostic support bundle: output directory is required")
	}
	absolute, absErr := filepath.Abs(value)
	if absErr != nil {
		return "", fmt.Errorf("diagnostic support bundle: resolve output: %w", absErr)
	}
	absolute = filepath.Clean(absolute)
	if _, lstatErr := os.Lstat(absolute); lstatErr == nil {
		return "", fmt.Errorf("diagnostic support bundle: output already exists")
	} else if !os.IsNotExist(lstatErr) {
		return "", fmt.Errorf("diagnostic support bundle: inspect output: %w", lstatErr)
	}
	parent := filepath.Dir(absolute)
	resolvedParent, resolveErr := filepath.EvalSymlinks(parent)
	if resolveErr != nil {
		return "", fmt.Errorf("diagnostic support bundle: resolve output parent: %w", resolveErr)
	}
	info, statErr := os.Stat(resolvedParent)
	if statErr != nil {
		return "", fmt.Errorf("diagnostic support bundle: inspect output parent: %w", statErr)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("diagnostic support bundle: output parent is not a directory")
	}
	return filepath.Join(resolvedParent, filepath.Base(absolute)), nil
}

func writePayload(root, relative string, data []byte, files *[]BundleFile) error {
	relative = filepath.FromSlash(relative)
	if !filepath.IsLocal(relative) {
		return fmt.Errorf("diagnostic support bundle: payload path is invalid")
	}
	path := filepath.Join(root, relative)
	directory := filepath.Dir(path)
	relativeDirectory := filepath.Dir(relative)
	if relativeDirectory != "." {
		if mkdirErr := fileutil.MkdirAllDurable(root, relativeDirectory, 0o700); mkdirErr != nil {
			return fmt.Errorf("diagnostic support bundle: create payload directory: %w", mkdirErr)
		}
	}
	if chmodErr := os.Chmod(directory, 0o700); chmodErr != nil {
		return fmt.Errorf("diagnostic support bundle: secure payload directory: %w", chmodErr)
	}
	if err := writeBundleFile(path, data); err != nil {
		return err
	}
	if syncErr := fileutil.SyncDirectory(directory); syncErr != nil {
		return fmt.Errorf("diagnostic support bundle: sync payload directory: %w", syncErr)
	}
	*files = append(*files, BundleFile{
		Path: filepath.ToSlash(relative), SizeBytes: len(data), SHA256: bytesSHA256(data), Mode: "0600",
	})
	return nil
}

func writeBundleFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("diagnostic support bundle: create file: %w", err)
	}
	writeErr := error(nil)
	if _, err := file.Write(data); err != nil {
		writeErr = err
	} else if err := file.Sync(); err != nil {
		writeErr = err
	}
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("diagnostic support bundle: write file: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("diagnostic support bundle: close file: %w", closeErr)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("diagnostic support bundle: secure file: %w", err)
	}
	return nil
}

func marshalBundleJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("diagnostic support bundle: encode JSON: %w", err)
	}
	return append(data, '\n'), nil
}

func bytesSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
