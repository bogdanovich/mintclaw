// Package diagnostics provides bounded, read-only discovery over passive
// diagnostic traces. It never resumes, retries, repairs, or replays runtime
// work.
package diagnostics

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
)

const (
	QuerySchemaV1       = "mintclaw.diagnostics.query.v1"
	DefaultResultLimit  = 20
	HardResultLimit     = 200
	DefaultMaxScanFiles = 500
	HardMaxStores       = 32
	HardTraceSelectors  = 64
)

var safeSelectorPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@+-]{0,255}$`)

type CaptureSettings struct {
	Enabled        bool   `json:"enabled"`
	ContentMode    string `json:"content_mode"`
	MaxTraceBytes  int    `json:"max_trace_bytes"`
	MaxRecords     int    `json:"max_records"`
	MaxRecordBytes int    `json:"max_record_bytes"`
	RetentionHours int    `json:"retention_hours"`
	MaxTraces      int    `json:"max_traces"`
}

// StoreSpec binds an internal filesystem root to a safe operator-visible
// identity. Root is never serialized.
type StoreSpec struct {
	ID                  string          `json:"id"`
	Kind                string          `json:"kind"`
	Alias               string          `json:"alias,omitempty"`
	Root                string          `json:"-"`
	RequiredSessionHash string          `json:"-"`
	Capture             CaptureSettings `json:"capture"`
	Retention           time.Duration   `json:"-"`
	MaxTraces           int             `json:"-"`
}

type Selector struct {
	TraceIDs       []string  `json:"trace_ids,omitempty"`
	ThreadID       string    `json:"thread_id,omitempty"`
	TurnID         string    `json:"turn_id,omitempty"`
	TaskID         string    `json:"task_id,omitempty"`
	SessionHash    string    `json:"session_hash,omitempty"`
	ModelAttemptID string    `json:"model_attempt_id,omitempty"`
	ToolCallID     string    `json:"tool_call_id,omitempty"`
	InvocationID   string    `json:"invocation_id,omitempty"`
	InteractionID  string    `json:"interaction_id,omitempty"`
	CompletionID   string    `json:"completion_id,omitempty"`
	DeliveryID     string    `json:"delivery_id,omitempty"`
	NotBefore      time.Time `json:"not_before,omitempty"`
	NotAfter       time.Time `json:"not_after,omitempty"`
}

func (s Selector) HasIdentity() bool {
	return len(s.TraceIDs) > 0 || s.ThreadID != "" || s.TurnID != "" || s.TaskID != "" ||
		s.SessionHash != "" || s.ModelAttemptID != "" || s.ToolCallID != "" ||
		s.InvocationID != "" || s.InteractionID != "" || s.CompletionID != "" || s.DeliveryID != ""
}

type QueryOptions struct {
	Stores       []StoreSpec
	Selector     Selector
	Limit        int
	MaxScanFiles int
	MaxScanBytes int64
	Now          func() time.Time
}

type StoreHealth struct {
	ID                  string          `json:"id"`
	Kind                string          `json:"kind"`
	Alias               string          `json:"alias,omitempty"`
	RootHash            string          `json:"root_hash"`
	Capture             CaptureSettings `json:"capture"`
	Status              string          `json:"status"`
	ScanMode            string          `json:"scan_mode"`
	ErrorCode           string          `json:"error_code,omitempty"`
	ExaminedEntries     int             `json:"examined_entries"`
	CandidateFiles      int             `json:"candidate_files"`
	ScannedFiles        int             `json:"scanned_files"`
	ScannedBytes        int64           `json:"scanned_bytes"`
	ValidTraces         int             `json:"valid_traces"`
	ProblemFiles        int             `json:"problem_files"`
	CorruptTraces       int             `json:"corrupt_traces"`
	ExpiredTraces       int             `json:"expired_traces"`
	IncompleteTraces    int             `json:"incomplete_traces"`
	DroppedRecords      int             `json:"dropped_records"`
	ScanTruncated       bool            `json:"scan_truncated,omitempty"`
	ScanTruncateReasons []string        `json:"scan_truncate_reasons,omitempty"`
}

type Problem struct {
	StoreID    string    `json:"store_id"`
	TraceID    string    `json:"trace_id,omitempty"`
	NameHash   string    `json:"name_hash,omitempty"`
	Code       string    `json:"code"`
	SizeBytes  int64     `json:"size_bytes,omitempty"`
	ModifiedAt time.Time `json:"modified_at,omitempty"`
}

type LocatedTrace struct {
	StoreID string
	Trace   diagnostictrace.Trace
}

type QueryResult struct {
	SchemaVersion  string         `json:"schema_version"`
	GeneratedAt    time.Time      `json:"generated_at"`
	Selector       Selector       `json:"selector"`
	Stores         []StoreHealth  `json:"stores"`
	Problems       []Problem      `json:"problems,omitempty"`
	Matches        []LocatedTrace `json:"-"`
	TotalMatches   int            `json:"total_matches"`
	MatchTruncated bool           `json:"match_truncated,omitempty"`
	ScanTruncated  bool           `json:"scan_truncated,omitempty"`
}

func Query(options QueryOptions) (QueryResult, error) {
	if err := validateSelector(options.Selector); err != nil {
		return QueryResult{}, err
	}
	if len(options.Stores) == 0 {
		return QueryResult{}, fmt.Errorf("diagnostics query: at least one trace store is required")
	}
	if len(options.Stores) > HardMaxStores {
		return QueryResult{}, fmt.Errorf("diagnostics query: trace stores exceed limit %d", HardMaxStores)
	}
	limit := options.Limit
	if limit <= 0 {
		limit = DefaultResultLimit
	}
	if limit > HardResultLimit {
		return QueryResult{}, fmt.Errorf("diagnostics query: result limit exceeds %d", HardResultLimit)
	}
	remainingFiles := options.MaxScanFiles
	if remainingFiles <= 0 {
		remainingFiles = DefaultMaxScanFiles
	}
	remainingFiles = min(remainingFiles, diagnostictrace.HardScanMaxFiles)
	remainingBytes := options.MaxScanBytes
	if remainingBytes <= 0 {
		remainingBytes = diagnostictrace.DefaultScanMaxBytes
	}
	remainingBytes = min(remainingBytes, diagnostictrace.HardScanMaxBytes)
	now := time.Now().UTC()
	if options.Now != nil {
		now = options.Now().UTC()
	}

	result := QueryResult{
		SchemaVersion: QuerySchemaV1,
		GeneratedAt:   now,
		Selector:      options.Selector,
		Stores:        make([]StoreHealth, 0, len(options.Stores)),
	}
	matches := make([]LocatedTrace, 0)
	seenRoots := make(map[string]struct{}, len(options.Stores))
	for _, spec := range options.Stores {
		if err := validateStoreSpec(spec); err != nil {
			return QueryResult{}, err
		}
		root, err := filepath.Abs(spec.Root)
		if err != nil {
			return QueryResult{}, fmt.Errorf("diagnostics query: resolve store %s: %w", spec.ID, err)
		}
		root = filepath.Clean(root)
		if _, duplicate := seenRoots[root]; duplicate {
			continue
		}
		seenRoots[root] = struct{}{}
		health := StoreHealth{
			ID: spec.ID, Kind: spec.Kind, Alias: spec.Alias,
			RootHash: pathHash(root), Capture: spec.Capture, Status: "available", ScanMode: "newest_bounded",
		}
		if remainingFiles <= 0 || remainingBytes <= 0 {
			health.Status = "not_scanned"
			health.ScanTruncated = true
			health.ScanTruncateReasons = []string{"global_scan_limit"}
			result.ScanTruncated = true
			result.Stores = append(result.Stores, health)
			continue
		}
		maximumFiles := spec.MaxTraces
		if maximumFiles <= 0 {
			maximumFiles = spec.Capture.MaxTraces
		}
		if maximumFiles <= 0 {
			maximumFiles = diagnostictrace.DefaultMaxTraces
		}
		maximumFiles = min(maximumFiles, remainingFiles)
		store := diagnostictrace.Store{
			Root: root, Retention: spec.Retention, MaxTraces: maximumFiles,
		}
		var scan diagnostictrace.ScanResult
		var scanErr error
		if len(options.Selector.TraceIDs) > 0 {
			health.ScanMode = "exact_trace_ids"
			scan, scanErr = store.ScanExact(
				options.Selector.TraceIDs,
				diagnostictrace.ScanOptions{MaxFiles: maximumFiles, MaxBytes: remainingBytes},
			)
		} else {
			scan, scanErr = store.Scan(
				diagnostictrace.ScanOptions{MaxFiles: maximumFiles, MaxBytes: remainingBytes},
			)
		}
		if scanErr != nil {
			health.Status = "unavailable"
			health.ErrorCode = classifyStoreError(scanErr)
			result.Stores = append(result.Stores, health)
			continue
		}
		remainingFiles -= scan.ScannedFiles
		remainingBytes -= scan.ScannedBytes
		health.ExaminedEntries = scan.ExaminedEntries
		health.CandidateFiles = scan.CandidateFiles
		health.ScannedFiles = scan.ScannedFiles
		health.ScannedBytes = scan.ScannedBytes
		health.ValidTraces = len(scan.Traces)
		health.ProblemFiles = len(scan.Problems)
		health.ScanTruncated = scan.Truncated
		health.ScanTruncateReasons = append([]string(nil), scan.Reasons...)
		result.ScanTruncated = result.ScanTruncated || scan.Truncated
		for _, problem := range scan.Problems {
			if problem.Code == "corrupt_trace" {
				health.CorruptTraces++
			}
			result.Problems = append(result.Problems, Problem{
				StoreID: spec.ID, TraceID: problem.TraceID, NameHash: problem.NameHash,
				Code: problem.Code, SizeBytes: problem.SizeBytes, ModifiedAt: problem.ModifiedAt,
			})
		}
		for _, trace := range scan.Traces {
			if trace.Truncation.Incomplete {
				health.IncompleteTraces++
			}
			health.DroppedRecords += trace.Truncation.DroppedRecords
			if spec.Retention > 0 && now.Sub(trace.CreatedAt) > spec.Retention {
				health.ExpiredTraces++
			}
			if matchesSelector(spec, trace, options.Selector) {
				matches = append(matches, LocatedTrace{StoreID: spec.ID, Trace: trace})
			}
		}
		result.Stores = append(result.Stores, health)
	}

	slices.SortFunc(matches, func(a, b LocatedTrace) int {
		if compared := b.Trace.CreatedAt.Compare(a.Trace.CreatedAt); compared != 0 {
			return compared
		}
		if compared := strings.Compare(a.Trace.TraceID, b.Trace.TraceID); compared != 0 {
			return compared
		}
		return strings.Compare(a.StoreID, b.StoreID)
	})
	result.TotalMatches = len(matches)
	if len(matches) > limit {
		matches = matches[:limit]
		result.MatchTruncated = true
	}
	result.Matches = matches
	return result, nil
}

func validateSelector(selector Selector) error {
	for _, traceID := range selector.TraceIDs {
		if !safeSelectorPattern.MatchString(traceID) || len(traceID) > 128 {
			return fmt.Errorf("diagnostics query: invalid trace ID")
		}
	}
	if len(selector.TraceIDs) > HardTraceSelectors {
		return fmt.Errorf("diagnostics query: trace selectors exceed limit %d", HardTraceSelectors)
	}
	values := []struct {
		name  string
		value string
	}{
		{"thread ID", selector.ThreadID},
		{"turn ID", selector.TurnID},
		{"task ID", selector.TaskID},
		{"model attempt ID", selector.ModelAttemptID},
		{"tool call ID", selector.ToolCallID},
		{"invocation ID", selector.InvocationID},
		{"interaction ID", selector.InteractionID},
		{"completion ID", selector.CompletionID},
		{"delivery ID", selector.DeliveryID},
	}
	for _, candidate := range values {
		if candidate.value != "" && !safeSelectorPattern.MatchString(candidate.value) {
			return fmt.Errorf("diagnostics query: invalid %s", candidate.name)
		}
	}
	if selector.SessionHash != "" && !validSHA256(selector.SessionHash) {
		return fmt.Errorf("diagnostics query: invalid session hash")
	}
	if !selector.NotBefore.IsZero() && !selector.NotAfter.IsZero() && selector.NotAfter.Before(selector.NotBefore) {
		return fmt.Errorf("diagnostics query: end time precedes start time")
	}
	return nil
}

func validateStoreSpec(spec StoreSpec) error {
	if !safeSelectorPattern.MatchString(spec.ID) {
		return fmt.Errorf("diagnostics query: invalid store ID")
	}
	if !safeSelectorPattern.MatchString(spec.Kind) {
		return fmt.Errorf("diagnostics query: invalid store kind")
	}
	if spec.Alias != "" && !safeSelectorPattern.MatchString(spec.Alias) {
		return fmt.Errorf("diagnostics query: invalid store alias")
	}
	if strings.TrimSpace(spec.Root) == "" {
		return fmt.Errorf("diagnostics query: store root is required")
	}
	if spec.RequiredSessionHash != "" && !validSHA256(spec.RequiredSessionHash) {
		return fmt.Errorf("diagnostics query: store session boundary is invalid")
	}
	return nil
}

func matchesSelector(spec StoreSpec, trace diagnostictrace.Trace, selector Selector) bool {
	if spec.RequiredSessionHash != "" && trace.Metadata.SessionHash != spec.RequiredSessionHash &&
		!traceHasRecordValue(trace, func(record diagnostictrace.Record) bool {
			return record.Scope.SessionHash == spec.RequiredSessionHash
		}) {
		return false
	}
	if len(selector.TraceIDs) > 0 && !slices.Contains(selector.TraceIDs, trace.TraceID) {
		return false
	}
	if selector.ThreadID != "" && (spec.Kind != "coding_thread" || spec.Alias != selector.ThreadID) {
		return false
	}
	if !selector.NotBefore.IsZero() && trace.CreatedAt.Before(selector.NotBefore) {
		return false
	}
	if !selector.NotAfter.IsZero() && trace.CreatedAt.After(selector.NotAfter) {
		return false
	}
	if selector.TurnID != "" && !traceHasTurn(trace, selector.TurnID) {
		return false
	}
	if selector.TaskID != "" && !traceHasRecordValue(trace, func(record diagnostictrace.Record) bool {
		return record.Scope.TaskID == selector.TaskID
	}) {
		return false
	}
	if selector.SessionHash != "" && trace.Metadata.SessionHash != selector.SessionHash &&
		!traceHasRecordValue(trace, func(record diagnostictrace.Record) bool {
			return record.Scope.SessionHash == selector.SessionHash
		}) {
		return false
	}
	correlations := []struct {
		value string
		read  func(diagnostictrace.Correlation) string
	}{
		{selector.ModelAttemptID, func(value diagnostictrace.Correlation) string { return value.ModelAttemptID }},
		{selector.ToolCallID, func(value diagnostictrace.Correlation) string { return value.ToolCallID }},
		{selector.InvocationID, func(value diagnostictrace.Correlation) string { return value.InvocationID }},
		{selector.InteractionID, func(value diagnostictrace.Correlation) string { return value.InteractionID }},
		{selector.CompletionID, func(value diagnostictrace.Correlation) string { return value.CompletionID }},
		{selector.DeliveryID, func(value diagnostictrace.Correlation) string { return value.DeliveryID }},
	}
	for _, expected := range correlations {
		if expected.value == "" {
			continue
		}
		if !traceHasRecordValue(trace, func(record diagnostictrace.Record) bool {
			return expected.read(record.Correlation) == expected.value
		}) {
			return false
		}
	}
	return true
}

func traceHasTurn(trace diagnostictrace.Trace, turnID string) bool {
	if trace.Metadata.RootTurnID == turnID || trace.Metadata.ParentTurnID == turnID ||
		trace.Metadata.ChildTurnID == turnID {
		return true
	}
	return traceHasRecordValue(trace, func(record diagnostictrace.Record) bool {
		return record.Scope.TurnID == turnID || record.Correlation.ParentTurnID == turnID
	})
}

func traceHasRecordValue(trace diagnostictrace.Trace, match func(diagnostictrace.Record) bool) bool {
	for _, record := range trace.Records {
		if match(record) {
			return true
		}
	}
	return false
}

func classifyStoreError(err error) string {
	if errors.Is(err, os.ErrPermission) {
		return "permission_denied"
	}
	return "store_unavailable"
}

func pathHash(path string) string {
	digest := sha256.Sum256([]byte(path))
	return hex.EncodeToString(digest[:])
}

func validSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
