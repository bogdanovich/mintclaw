package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const RepositoryBaselineSchemaV1 = "mintclaw.repository_baseline.v1"

type BaselineOrigin string

const (
	BaselineOriginNew            BaselineOrigin = "new_thread"
	BaselineOriginResumeAdoption BaselineOrigin = "resume_adoption"
	BaselineOriginFork           BaselineOrigin = "fork"
)

type BaselineRequest struct {
	ProjectKey string
	Origin     BaselineOrigin
	CapturedAt time.Time
}

type BaselinePath struct {
	Path              string `json:"path"`
	OriginalPath      string `json:"original_path,omitempty"`
	Status            string `json:"status"`
	Fingerprint       string `json:"fingerprint,omitempty"`
	Size              int64  `json:"size,omitempty"`
	Missing           bool   `json:"missing,omitempty"`
	Symlink           bool   `json:"symlink,omitempty"`
	EvidenceComplete  bool   `json:"evidence_complete"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

type BaselineLimits struct {
	ChangedPaths     int   `json:"changed_paths"`
	CommandBytes     int   `json:"command_bytes"`
	FingerprintBytes int   `json:"fingerprint_bytes"`
	TimeoutMillis    int64 `json:"timeout_millis"`
}

type RepositoryBaseline struct {
	SchemaVersion       string         `json:"schema_version"`
	BaselineID          string         `json:"baseline_id"`
	ProjectKey          string         `json:"project_key"`
	Origin              BaselineOrigin `json:"origin"`
	CapturedAt          time.Time      `json:"captured_at"`
	RepositoryAvailable bool           `json:"repository_available"`
	TopLevel            string         `json:"top_level,omitempty"`
	CommonDir           string         `json:"common_dir,omitempty"`
	Head                string         `json:"head,omitempty"`
	Branch              string         `json:"branch,omitempty"`
	Detached            bool           `json:"detached,omitempty"`
	Unborn              bool           `json:"unborn,omitempty"`
	Generation          string         `json:"generation,omitempty"`
	IndexFingerprint    string         `json:"index_fingerprint,omitempty"`
	IndexComplete       bool           `json:"index_complete"`
	Limits              BaselineLimits `json:"limits"`
	Paths               []BaselinePath `json:"paths,omitempty"`
	PathsComplete       bool           `json:"paths_complete"`
	Truncated           bool           `json:"truncated,omitempty"`
	UnavailableReason   string         `json:"unavailable_reason,omitempty"`
	Warning             string         `json:"warning,omitempty"`
}

func (repository *Repository) CaptureBaseline(
	ctx context.Context,
	request BaselineRequest,
) (RepositoryBaseline, error) {
	if repository == nil {
		return RepositoryBaseline{}, fmt.Errorf("repository baseline: evidence service is unavailable")
	}
	if err := validateBaselineRequest(request); err != nil {
		return RepositoryBaseline{}, err
	}
	if !repository.acquire(ctx) {
		return RepositoryBaseline{}, contextError(ctx)
	}
	defer repository.release()

	commandCtx, cancel := context.WithTimeout(contextOrBackground(ctx), repository.limits.Timeout)
	defer cancel()
	snapshot := Capture(commandCtx, repository.projectRoot, repository.cwd, repository.limits)
	baseline := RepositoryBaseline{
		SchemaVersion: RepositoryBaselineSchemaV1,
		ProjectKey:    request.ProjectKey, Origin: request.Origin, CapturedAt: request.CapturedAt.UTC(),
		RepositoryAvailable: snapshot.Git.Available,
		TopLevel:            snapshot.Git.TopLevel, CommonDir: snapshot.Git.CommonDir,
		Head: snapshot.Git.Head, Branch: snapshot.Git.Branch,
		Detached: snapshot.Git.Detached, Unborn: snapshot.Git.Unborn,
		Generation: snapshot.Identity(), PathsComplete: snapshot.Git.StatusAvailable && !snapshot.Truncated,
		Limits: BaselineLimits{
			ChangedPaths: repository.limits.ChangedPaths, CommandBytes: repository.limits.CommandBytes,
			FingerprintBytes: repository.limits.UntrackedBytes,
			TimeoutMillis:    repository.limits.Timeout.Milliseconds(),
		},
		Truncated: snapshot.Truncated,
	}
	if snapshot.Git.UnavailableReason != "" {
		baseline.UnavailableReason = "repository evidence is unavailable"
	}
	if snapshot.Warning != "" {
		baseline.Warning = "repository status capture reported a warning"
	}
	if snapshot.Git.Available && !snapshot.Git.StatusAvailable && baseline.UnavailableReason == "" {
		baseline.UnavailableReason = "Git status is unavailable"
	}
	for _, changed := range snapshot.ChangedPaths {
		baseline.Paths = append(baseline.Paths, captureBaselinePath(snapshot.Git.TopLevel, changed, repository.limits))
	}
	var indexFilters []string
	if snapshot.Git.Available {
		filters, warning, safe, truncated := passiveFilterOverrides(
			commandCtx,
			snapshot.ProjectRoot,
			repository.limits.CommandBytes,
		)
		if warning != "" {
			baseline.Warning = joinWarning(baseline.Warning, "Git content-filter inspection was incomplete")
		}
		baseline.Truncated = baseline.Truncated || truncated
		if safe {
			indexFilters = filters
			baseline.IndexFingerprint, baseline.IndexComplete, truncated = captureIndexFingerprint(
				commandCtx,
				snapshot.ProjectRoot,
				repository.limits.CommandBytes,
				filters,
			)
			baseline.Truncated = baseline.Truncated || truncated
			if !baseline.IndexComplete {
				baseline.Warning = joinWarning(baseline.Warning, "staged identity is unavailable")
			}
		}
	}
	if snapshot.Git.Available {
		after := Capture(commandCtx, repository.projectRoot, repository.cwd, repository.limits)
		if snapshot.Identity() != after.Identity() {
			baseline.PathsComplete = false
			baseline.IndexComplete = false
			baseline.IndexFingerprint = ""
			baseline.Truncated = true
			baseline.Warning = joinWarning(
				baseline.Warning,
				"repository changed while baseline evidence was captured",
			)
		}
		if baseline.IndexComplete {
			afterFingerprint, afterComplete, afterTruncated := captureIndexFingerprint(
				commandCtx,
				snapshot.ProjectRoot,
				repository.limits.CommandBytes,
				indexFilters,
			)
			baseline.Truncated = baseline.Truncated || afterTruncated
			if !afterComplete || afterFingerprint != baseline.IndexFingerprint {
				baseline.IndexComplete = false
				baseline.IndexFingerprint = ""
				baseline.Truncated = true
				baseline.Warning = joinWarning(
					baseline.Warning,
					"staged identity changed while baseline evidence was captured",
				)
			}
		}
	}
	if err := commandCtx.Err(); err != nil {
		baseline.PathsComplete = false
		baseline.IndexComplete = false
		baseline.IndexFingerprint = ""
		baseline.Truncated = true
		baseline.Warning = joinWarning(baseline.Warning, "repository baseline capture: "+err.Error())
	}
	baseline.BaselineID = baselineDigest(baseline)
	return baseline, baseline.Validate()
}

func captureIndexFingerprint(
	ctx context.Context,
	root string,
	limit int,
	filters []string,
) (string, bool, bool) {
	index, err := runGitWithConfig(ctx, root, limit, filters, "ls-files", "--stage", "-z")
	if err != nil || index.truncated || len(index.stdout) > 0 && index.stdout[len(index.stdout)-1] != 0 {
		return "", false, index.truncated
	}
	digest := sha256.Sum256(index.stdout)
	return hex.EncodeToString(digest[:]), true, false
}

func (repository *Repository) Provenance(
	ctx context.Context,
	baseline RepositoryBaseline,
	observedAt time.Time,
) (ProvenanceResult, error) {
	if err := baseline.Validate(); err != nil {
		return ProvenanceResult{}, err
	}
	current, err := repository.CaptureBaseline(ctx, BaselineRequest{
		ProjectKey: baseline.ProjectKey,
		Origin:     BaselineOriginNew,
		CapturedAt: observedAt,
	})
	if err != nil {
		return ProvenanceResult{}, err
	}
	return CompareBaseline(baseline, current), nil
}

func captureBaselinePath(root string, changed ChangedPath, limits Limits) BaselinePath {
	path := BaselinePath{Path: changed.Path, OriginalPath: changed.OriginalPath, Status: changed.Status}
	content, info, truncated, reason := readPassiveRegularFile(root, changed.Path, limits.UntrackedBytes)
	if reason != "" {
		path.Symlink = info != nil && info.Mode()&os.ModeSymlink != 0
		if strings.Contains(changed.Status, "D") && info == nil && reason == "untracked path is unavailable" {
			path.Missing = true
			path.EvidenceComplete = true
			return path
		}
		path.UnavailableReason = reason
		return path
	}
	path.Size = info.Size()
	if truncated {
		path.UnavailableReason = "file exceeds baseline fingerprint byte limit"
		return path
	}
	digest := sha256.Sum256(content)
	path.Fingerprint = hex.EncodeToString(digest[:])
	path.EvidenceComplete = true
	return path
}

func validateBaselineRequest(request BaselineRequest) error {
	if strings.TrimSpace(request.ProjectKey) == "" || request.ProjectKey != strings.TrimSpace(request.ProjectKey) ||
		len(request.ProjectKey) > 256 || !utf8.ValidString(request.ProjectKey) {
		return fmt.Errorf("repository baseline: project key is required within 256 bytes")
	}
	switch request.Origin {
	case BaselineOriginNew, BaselineOriginResumeAdoption, BaselineOriginFork:
	default:
		return fmt.Errorf("repository baseline: unsupported origin %q", request.Origin)
	}
	if request.CapturedAt.IsZero() {
		return fmt.Errorf("repository baseline: capture time is required")
	}
	return nil
}

func (baseline RepositoryBaseline) Validate() error {
	if baseline.SchemaVersion != RepositoryBaselineSchemaV1 {
		return fmt.Errorf("repository baseline: unsupported schema %q", baseline.SchemaVersion)
	}
	if err := validateBaselineRequest(BaselineRequest{
		ProjectKey: baseline.ProjectKey,
		Origin:     baseline.Origin,
		CapturedAt: baseline.CapturedAt,
	}); err != nil {
		return err
	}
	if baseline.CapturedAt.Location() != time.UTC {
		return fmt.Errorf("repository baseline: capture time must be UTC")
	}
	if len(baseline.BaselineID) != 64 || baseline.BaselineID != baselineDigest(baseline) {
		return fmt.Errorf("repository baseline: baseline ID does not match evidence")
	}
	if baseline.RepositoryAvailable {
		if !filepath.IsAbs(baseline.TopLevel) || !filepath.IsAbs(baseline.CommonDir) ||
			filepath.Clean(
				baseline.TopLevel,
			) != baseline.TopLevel || filepath.Clean(baseline.CommonDir) != baseline.CommonDir {
			return fmt.Errorf("repository baseline: repository paths must be clean and absolute")
		}
	}
	if baseline.Generation != "" && (len(baseline.Generation) != 64 || !isHexDigest(baseline.Generation)) {
		return fmt.Errorf("repository baseline: generation is invalid")
	}
	if baseline.Head != "" &&
		((len(baseline.Head) != 40 && len(baseline.Head) != 64) || !isHexDigest(baseline.Head)) {
		return fmt.Errorf("repository baseline: HEAD is invalid")
	}
	if baseline.IndexFingerprint != "" &&
		(len(baseline.IndexFingerprint) != 64 || !isHexDigest(baseline.IndexFingerprint)) {
		return fmt.Errorf("repository baseline: index fingerprint is invalid")
	}
	if baseline.IndexComplete != (baseline.IndexFingerprint != "") {
		return fmt.Errorf("repository baseline: index completeness does not match its fingerprint")
	}
	if len(baseline.UnavailableReason) > 4096 || len(baseline.Warning) > 4096 {
		return fmt.Errorf("repository baseline: diagnostic text exceeds bounds")
	}
	if len(baseline.Branch) > 4096 || !utf8.ValidString(baseline.Branch) ||
		!utf8.ValidString(baseline.UnavailableReason) || !utf8.ValidString(baseline.Warning) {
		return fmt.Errorf("repository baseline: text metadata is invalid")
	}
	if baseline.Limits.ChangedPaths <= 0 || baseline.Limits.CommandBytes <= 0 ||
		baseline.Limits.FingerprintBytes <= 0 || baseline.Limits.TimeoutMillis <= 0 ||
		len(baseline.Paths) > baseline.Limits.ChangedPaths {
		return fmt.Errorf("repository baseline: capture limits are invalid")
	}
	for _, path := range baseline.Paths {
		if path.Path == "" || len(path.Path) > 4096 || !utf8.ValidString(path.Path) ||
			!filepath.IsLocal(path.Path) || strings.ContainsRune(path.Path, 0) {
			return fmt.Errorf("repository baseline: changed path is invalid")
		}
		if path.OriginalPath != "" &&
			(len(path.OriginalPath) > 4096 || !utf8.ValidString(path.OriginalPath) ||
				!filepath.IsLocal(path.OriginalPath) || strings.ContainsRune(path.OriginalPath, 0)) {
			return fmt.Errorf("repository baseline: original path is invalid")
		}
		if path.Status == "" || len(path.Status) > 16 || strings.ContainsAny(path.Status, "\x00\r\n") {
			return fmt.Errorf("repository baseline: path status is invalid")
		}
		if path.Fingerprint != "" && (len(path.Fingerprint) != 64 || !isHexDigest(path.Fingerprint)) {
			return fmt.Errorf("repository baseline: path fingerprint is invalid")
		}
		if path.Size < 0 || len(path.UnavailableReason) > 4096 || !utf8.ValidString(path.UnavailableReason) {
			return fmt.Errorf("repository baseline: path evidence is invalid")
		}
		if path.EvidenceComplete != (path.Missing || path.Fingerprint != "") ||
			path.Missing && path.Fingerprint != "" || path.EvidenceComplete && path.UnavailableReason != "" {
			return fmt.Errorf("repository baseline: path completeness is inconsistent")
		}
	}
	return nil
}

func baselineDigest(baseline RepositoryBaseline) string {
	baseline.BaselineID = ""
	data, _ := json.Marshal(baseline)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func isHexDigest(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

type ProvenanceKind string

const (
	ProvenancePreExisting               ProvenanceKind = "pre_existing"
	ProvenanceFirstObservedDuringThread ProvenanceKind = "first_observed_during_thread"
	ProvenanceResolvedSinceBaseline     ProvenanceKind = "resolved_since_baseline"
	ProvenanceIndeterminate             ProvenanceKind = "indeterminate"
)

type ProvenancePath struct {
	Path         string         `json:"path"`
	OriginalPath string         `json:"original_path,omitempty"`
	Status       string         `json:"status,omitempty"`
	Provenance   ProvenanceKind `json:"provenance"`
	Reason       string         `json:"reason,omitempty"`
}

type ProvenanceResult struct {
	BaselineID        string           `json:"baseline_id"`
	CurrentGeneration string           `json:"current_generation,omitempty"`
	Paths             []ProvenancePath `json:"paths,omitempty"`
	Indeterminate     bool             `json:"indeterminate,omitempty"`
	Reason            string           `json:"reason,omitempty"`
}

func CompareBaseline(baseline, current RepositoryBaseline) ProvenanceResult {
	result := ProvenanceResult{BaselineID: baseline.BaselineID, CurrentGeneration: current.Generation}
	reason := baselineComparisonReason(baseline, current)
	result.Indeterminate = reason != ""
	baselinePaths := baselinePathMap(baseline.Paths)
	currentPaths := baselinePathMap(current.Paths)
	keys := make([]string, 0, len(baselinePaths)+len(currentPaths))
	for key := range baselinePaths {
		keys = append(keys, key)
	}
	for key := range currentPaths {
		if _, exists := baselinePaths[key]; !exists {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		before, beforeExists := baselinePaths[key]
		after, afterExists := currentPaths[key]
		candidate := after
		if !afterExists {
			candidate = before
		}
		path := ProvenancePath{Path: candidate.Path, OriginalPath: candidate.OriginalPath, Status: candidate.Status}
		if reason != "" {
			path.Provenance, path.Reason = ProvenanceIndeterminate, reason
		} else {
			switch {
			case beforeExists && afterExists && baselinePathEqual(before, after):
				path.Provenance = ProvenancePreExisting
			case beforeExists && !afterExists && current.PathsComplete:
				path.Provenance = ProvenanceResolvedSinceBaseline
			case !beforeExists && afterExists && baseline.PathsComplete && after.EvidenceComplete:
				path.Provenance = ProvenanceFirstObservedDuringThread
			case beforeExists && afterExists && before.EvidenceComplete && after.EvidenceComplete:
				path.Provenance = ProvenanceFirstObservedDuringThread
			default:
				path.Provenance = ProvenanceIndeterminate
				path.Reason = "bounded evidence cannot prove the path transition"
			}
		}
		result.Indeterminate = result.Indeterminate || path.Provenance == ProvenanceIndeterminate
		result.Paths = append(result.Paths, path)
	}
	result.Reason = reason
	return result
}

func baselineComparisonReason(baseline, current RepositoryBaseline) string {
	if baseline.Validate() != nil || current.Validate() != nil {
		return "baseline or current evidence is invalid"
	}
	if baseline.Origin == BaselineOriginResumeAdoption {
		return "legacy resume adoption cannot prove original-thread provenance"
	}
	if baseline.ProjectKey != current.ProjectKey || baseline.TopLevel != current.TopLevel ||
		baseline.CommonDir != current.CommonDir {
		return "repository authority changed since baseline"
	}
	if baseline.Head != current.Head || baseline.Unborn != current.Unborn {
		return "repository HEAD lineage changed since baseline"
	}
	if !baseline.RepositoryAvailable || !current.RepositoryAvailable {
		return "repository evidence is unavailable"
	}
	if !baseline.PathsComplete || !current.PathsComplete {
		return "changed-path evidence is incomplete"
	}
	if !baseline.IndexComplete || !current.IndexComplete {
		return "staged identity evidence is incomplete"
	}
	if baseline.IndexFingerprint != current.IndexFingerprint {
		return "staged identity changed since baseline"
	}
	return ""
}

func baselinePathMap(paths []BaselinePath) map[string]BaselinePath {
	result := make(map[string]BaselinePath, len(paths))
	for _, path := range paths {
		result[path.OriginalPath+"\x00"+path.Path] = path
	}
	return result
}

func baselinePathEqual(left, right BaselinePath) bool {
	if left.Path != right.Path || left.OriginalPath != right.OriginalPath || left.Status != right.Status ||
		left.Missing != right.Missing || left.Symlink != right.Symlink {
		return false
	}
	if !left.EvidenceComplete || !right.EvidenceComplete {
		return false
	}
	return left.Fingerprint == right.Fingerprint && left.Size == right.Size
}
