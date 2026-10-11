package diagnostictrace

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const SafeTraceSchemaV1 = "mintclaw.diagnostic_trace_summary.v1"

var (
	safeExportTokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@+-]{0,255}$`)
	safeExportCodePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

// SafeTrace is the metadata-only support projection of a validated trace. It
// intentionally omits every rich preview and does not preserve unknown data
// fields.
type SafeTrace struct {
	SchemaVersion string         `json:"schema_version"`
	TraceID       string         `json:"trace_id"`
	CreatedAt     time.Time      `json:"created_at"`
	Source        Source         `json:"source"`
	Policy        CapturePolicy  `json:"policy"`
	Limits        AppliedLimits  `json:"limits"`
	Metadata      Metadata       `json:"metadata,omitempty"`
	Records       []SafeRecord   `json:"records"`
	Outcome       *Outcome       `json:"outcome,omitempty"`
	Truncation    Truncation     `json:"truncation,omitempty"`
	Projection    SafeProjection `json:"projection"`
	RecordKinds   map[string]int `json:"record_kinds"`
}

type SafeProjection struct {
	RecordDataMode      string `json:"record_data_mode"`
	RichPreviewsOmitted bool   `json:"rich_previews_omitted"`
	UnsafeFieldsOmitted int    `json:"unsafe_fields_omitted"`
}

type SafeRecord struct {
	Sequence    uint64         `json:"sequence"`
	OffsetNanos int64          `json:"offset_nanos"`
	Kind        RecordKind     `json:"kind"`
	Origin      Origin         `json:"origin"`
	Scope       Scope          `json:"scope,omitempty"`
	Correlation Correlation    `json:"correlation,omitempty"`
	Digest      string         `json:"digest"`
	Facts       map[string]any `json:"facts,omitempty"`
}

// Summarize returns a content-free, allowlisted projection suitable for a
// default support bundle. Historical trace values are treated as untrusted:
// strings that could encode a path, command, or free-form content are omitted.
func Summarize(trace Trace) (SafeTrace, error) {
	if err := Validate(trace); err != nil {
		return SafeTrace{}, fmt.Errorf("summarize trace: %w", err)
	}
	omitted := 0
	source := Source{Commit: trace.Source.Commit}
	source.MintClawVersion = safeExportToken(trace.Source.MintClawVersion, &omitted)
	policy := CapturePolicy{ContentMode: trace.Policy.ContentMode}
	policy.Redactor = safeExportToken(trace.Policy.Redactor, &omitted)
	metadata := sanitizeMetadata(trace.Metadata, &omitted)
	records := make([]SafeRecord, 0, len(trace.Records))
	kinds := make(map[string]int)
	for _, record := range trace.Records {
		facts, factOmissions := safeRecordFacts(record.Data)
		omitted += factOmissions
		records = append(records, SafeRecord{
			Sequence:    record.Sequence,
			OffsetNanos: record.OffsetNanos,
			Kind:        record.Kind,
			Origin:      sanitizeOrigin(record.Origin, &omitted),
			Scope:       sanitizeScope(record.Scope, &omitted),
			Correlation: sanitizeCorrelation(record.Correlation, &omitted),
			Digest:      record.Digest,
			Facts:       facts,
		})
		kinds[string(record.Kind)]++
	}
	return SafeTrace{
		SchemaVersion: SafeTraceSchemaV1,
		TraceID:       trace.TraceID,
		CreatedAt:     trace.CreatedAt,
		Source:        source,
		Policy:        policy,
		Limits:        trace.Limits,
		Metadata:      metadata,
		Records:       records,
		Outcome:       sanitizeOutcome(trace.Outcome, &omitted),
		Truncation:    sanitizeTruncation(trace.Truncation, &omitted),
		Projection: SafeProjection{
			RecordDataMode:      "allowlisted_metadata",
			RichPreviewsOmitted: true,
			UnsafeFieldsOmitted: omitted,
		},
		RecordKinds: kinds,
	}, nil
}

func sanitizeMetadata(metadata Metadata, omitted *int) Metadata {
	return Metadata{
		RootTurnID:   safeExportToken(metadata.RootTurnID, omitted),
		ParentTurnID: safeExportToken(metadata.ParentTurnID, omitted),
		ChildTurnID:  safeExportToken(metadata.ChildTurnID, omitted),
		SessionHash:  safeSHA256(metadata.SessionHash, omitted),
		AgentID:      safeExportToken(metadata.AgentID, omitted),
		RuntimeID:    safeExportToken(metadata.RuntimeID, omitted),
		ProjectHash:  safeSHA256(metadata.ProjectHash, omitted),
	}
}

func sanitizeOrigin(origin Origin, omitted *int) Origin {
	return Origin{
		Kind: safeExportCode(origin.Kind, omitted),
		ID:   safeExportToken(origin.ID, omitted),
	}
}

func sanitizeScope(scope Scope, omitted *int) Scope {
	return Scope{
		AgentID:      safeExportToken(scope.AgentID, omitted),
		SessionHash:  safeSHA256(scope.SessionHash, omitted),
		TurnID:       safeExportToken(scope.TurnID, omitted),
		TaskID:       safeExportToken(scope.TaskID, omitted),
		Channel:      safeExportToken(scope.Channel, omitted),
		TargetHash:   safeSHA256(scope.TargetHash, omitted),
		TargetAlias:  safeExportToken(scope.TargetAlias, omitted),
		ScopeAlias:   safeExportToken(scope.ScopeAlias, omitted),
		ProfileAlias: safeExportToken(scope.ProfileAlias, omitted),
	}
}

func sanitizeCorrelation(correlation Correlation, omitted *int) Correlation {
	return Correlation{
		ParentTurnID:           safeExportToken(correlation.ParentTurnID, omitted),
		ModelAttemptID:         safeExportToken(correlation.ModelAttemptID, omitted),
		RequestID:              safeExportToken(correlation.RequestID, omitted),
		ToolCallID:             safeExportToken(correlation.ToolCallID, omitted),
		ToolCallGeneration:     safeExportToken(correlation.ToolCallGeneration, omitted),
		TaskGenerationID:       safeExportToken(correlation.TaskGenerationID, omitted),
		CompletionID:           safeExportToken(correlation.CompletionID, omitted),
		WorkerGenerationID:     safeExportToken(correlation.WorkerGenerationID, omitted),
		RemoteThreadID:         safeExportToken(correlation.RemoteThreadID, omitted),
		InvocationID:           safeExportToken(correlation.InvocationID, omitted),
		PolicyRevision:         safeExportToken(correlation.PolicyRevision, omitted),
		InteractionID:          safeExportToken(correlation.InteractionID, omitted),
		InteractionRevision:    safeNonNegativeInt64(correlation.InteractionRevision, omitted),
		InteractionEventID:     safeExportToken(correlation.InteractionEventID, omitted),
		CompactionAttemptID:    safeExportToken(correlation.CompactionAttemptID, omitted),
		RepositorySnapshotHash: safeSHA256(correlation.RepositorySnapshotHash, omitted),
		RepositoryCommit:       safeCommit(correlation.RepositoryCommit, omitted),
		DeliveryID:             safeExportToken(correlation.DeliveryID, omitted),
		EventID:                safeExportToken(correlation.EventID, omitted),
	}
}

func sanitizeOutcome(outcome *Outcome, omitted *int) *Outcome {
	if outcome == nil {
		return nil
	}
	return &Outcome{
		Status:      safeExportCode(outcome.Status, omitted),
		ContentHash: safeSHA256(outcome.ContentHash, omitted),
		ContentLen:  safeNonNegativeInt(outcome.ContentLen, omitted),
		ErrorCode:   safeExportCode(outcome.ErrorCode, omitted),
	}
}

func sanitizeTruncation(truncation Truncation, omitted *int) Truncation {
	reasons := make([]string, 0, len(truncation.Reasons))
	for _, reason := range truncation.Reasons {
		if safe := safeExportCode(reason, omitted); safe != "" {
			reasons = append(reasons, safe)
		}
	}
	dropped := make(map[RecordKind]int, len(truncation.DroppedByKind))
	for kind, count := range truncation.DroppedByKind {
		if _, supported := supportedKinds[kind]; supported && count >= 0 {
			dropped[kind] = count
		} else {
			(*omitted)++
		}
	}
	if len(dropped) == 0 {
		dropped = nil
	}
	return Truncation{
		Incomplete:     truncation.Incomplete,
		DroppedRecords: safeNonNegativeInt(truncation.DroppedRecords, omitted),
		Reasons:        reasons,
		DroppedByKind:  dropped,
	}
}

var safeNameFactKeys = map[string]struct{}{
	"provider": {}, "model": {}, "identity_key": {}, "stable_prefix_version": {},
	"request_id": {}, "tool": {}, "agent_id": {}, "child_turn_id": {},
}

var safeCodeFactKeys = map[string]struct{}{
	"status": {}, "reason": {}, "prompt_cache_mode": {}, "cache_outcome": {},
	"error_code": {}, "classification_source": {}, "provider_error_kind": {},
	"action": {}, "decision_code": {}, "role": {}, "mode": {}, "task_status": {},
	"remote_state": {}, "failure_code": {}, "kind": {}, "event": {}, "outcome": {},
	"code": {}, "snapshot_type": {}, "state": {}, "stage": {},
}

var safeHashFactKeys = map[string]struct{}{
	"input_hash": {}, "final_hash": {}, "prompt_hash": {}, "stable_prefix_hash": {},
	"stable_system_hash": {}, "dynamic_system_hash": {}, "tool_schema_hash": {},
	"history_hash": {}, "dynamic_tail_hash": {}, "response_hash": {}, "args_hash": {},
	"result_hash": {}, "message_hash": {}, "target_hash": {}, "snapshot_hash": {},
}

var safeIntegerFactKeys = map[string]struct{}{
	"input_len": {}, "final_len": {}, "iterations": {}, "attempt": {}, "messages": {},
	"tools": {}, "stable_system_parts": {}, "dynamic_system_parts": {},
	"history_messages": {}, "dynamic_tail_messages": {}, "prompt_tokens": {},
	"response_tokens": {}, "cache_read_input_tokens": {}, "cache_write_input_tokens": {},
	"http_status": {}, "retry_after_ms": {}, "count": {}, "threshold": {},
	"content_len": {}, "sequence": {}, "before_messages": {}, "after_messages": {},
	"transcript_revision": {}, "transcript_count": {}, "tokens_before": {},
	"tokens_after": {}, "tokens_saved": {}, "summaries_created": {},
	"leaf_summaries": {}, "condensed_summaries": {}, "duration_ms": {},
	"changed_files": {}, "added_lines": {}, "deleted_lines": {}, "binary_files": {},
	"active": {}, "limit": {}, "wait_ms": {}, "timeout_ms": {},
}

var safeBooleanFactKeys = map[string]struct{}{
	"tail_boundary_found": {}, "dynamic_system_before_transcript": {}, "cache_read_known": {},
	"cache_write_known": {}, "skipped": {}, "executed": {}, "is_error": {},
	"will_user": {}, "will_parent": {}, "success": {}, "token_counts_observed": {},
	"background": {}, "repository_available": {}, "repository_status_available": {},
	"repository_detached": {}, "repository_unborn": {}, "repository_worktree": {},
	"repository_dirty": {}, "diff_stat_available": {}, "submodule_worktree_state_ignored": {},
	"snapshot_truncated": {},
}

func safeRecordFacts(data json.RawMessage) (map[string]any, int) {
	if len(data) == 0 {
		return nil, 0
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var values map[string]any
	if err := decoder.Decode(&values); err != nil {
		return nil, 1
	}
	facts := make(map[string]any)
	omitted := 0
	for key, value := range values {
		switch {
		case hasKey(safeNameFactKeys, key):
			text, ok := value.(string)
			if !ok {
				omitted++
				continue
			}
			if safe := safeExportToken(text, &omitted); safe != "" {
				facts[key] = safe
			}
		case hasKey(safeCodeFactKeys, key):
			text, ok := value.(string)
			if !ok {
				omitted++
				continue
			}
			if safe := safeExportCode(text, &omitted); safe != "" {
				facts[key] = safe
			}
		case hasKey(safeHashFactKeys, key):
			text, ok := value.(string)
			if !ok {
				omitted++
				continue
			}
			if safe := safeSHA256(text, &omitted); safe != "" {
				facts[key] = safe
			}
		case hasKey(safeIntegerFactKeys, key):
			number, ok := value.(json.Number)
			if !ok {
				omitted++
				continue
			}
			integer, err := strconv.ParseInt(number.String(), 10, 64)
			if err != nil || integer < 0 {
				omitted++
				continue
			}
			facts[key] = integer
		case hasKey(safeBooleanFactKeys, key):
			boolean, ok := value.(bool)
			if !ok {
				omitted++
				continue
			}
			facts[key] = boolean
		default:
			omitted++
		}
	}
	if len(facts) == 0 {
		return nil, omitted
	}
	return facts, omitted
}

func safeNonNegativeInt(value int, omitted *int) int {
	if value < 0 {
		(*omitted)++
		return 0
	}
	return value
}

func safeNonNegativeInt64(value int64, omitted *int) int64 {
	if value < 0 {
		(*omitted)++
		return 0
	}
	return value
}

func safeExportToken(value string, omitted *int) string {
	if value == "" {
		return ""
	}
	if value != strings.TrimSpace(value) || !safeExportTokenPattern.MatchString(value) {
		(*omitted)++
		return ""
	}
	return value
}

func safeExportCode(value string, omitted *int) string {
	if value == "" {
		return ""
	}
	if value != strings.TrimSpace(value) || !safeExportCodePattern.MatchString(value) {
		(*omitted)++
		return ""
	}
	return value
}

func safeSHA256(value string, omitted *int) string {
	if value == "" {
		return ""
	}
	if len(value) != sha256HexLength || value != strings.ToLower(value) {
		(*omitted)++
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		(*omitted)++
		return ""
	}
	return value
}

func safeCommit(value string, omitted *int) string {
	if value == "" {
		return ""
	}
	if !validHexRange(value, 7, 64) {
		(*omitted)++
		return ""
	}
	return value
}

func hasKey(values map[string]struct{}, key string) bool {
	_, ok := values[key]
	return ok
}
