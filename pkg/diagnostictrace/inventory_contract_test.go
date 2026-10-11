package diagnostictrace_test

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

const codingInventorySchemaV1 = "mintclaw.coding_diagnostic_inventory.v1"

var requiredCodingDiagnosticPathIDs = []string{
	"local_coding_turn",
	"provider_retry_and_fallback",
	"tool_and_process_lifecycle",
	"compaction_and_resume",
	"human_interaction",
	"async_task_and_channel_delivery",
	"remote_coding_task",
	"companion_capability_invocation",
	"repository_and_worktree_state",
	"tui_and_headless_projection",
	"diagnostic_pipeline_health",
}

type codingDiagnosticInventory struct {
	SchemaVersion  string                 `json:"schema_version"`
	BaselineCommit string                 `json:"baseline_commit"`
	Paths          []codingDiagnosticPath `json:"paths"`
}

type codingDiagnosticPath struct {
	ID                 string                `json:"id"`
	Support            string                `json:"support"`
	Authorities        []string              `json:"authorities"`
	RuntimeEvents      []string              `json:"runtime_events"`
	TraceRecords       []string              `json:"trace_records"`
	RequiredIDs        []string              `json:"required_ids"`
	AvailableIDs       []string              `json:"available_ids"`
	MissingTraceJoins  []codingDiagnosticGap `json:"missing_trace_joins"`
	KnownLifecycleGaps []codingDiagnosticGap `json:"known_lifecycle_gaps"`
	TerminalFacts      []string              `json:"terminal_facts"`
	RetentionOwner     string                `json:"retention_owner"`
	ExportClass        string                `json:"export_class"`
}

type codingDiagnosticGap struct {
	ID          string `json:"id"`
	Disposition string `json:"disposition"`
	Observation string `json:"observation"`
}

func TestCodingDiagnosticInventoryContract(t *testing.T) {
	data, err := os.ReadFile("testdata/coding_path_inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	var inventory codingDiagnosticInventory
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&inventory); err != nil {
		t.Fatalf("decode coding diagnostic inventory: %v", err)
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("coding diagnostic inventory has trailing JSON: %v", err)
	}
	if inventory.SchemaVersion != codingInventorySchemaV1 {
		t.Fatalf("schema_version = %q", inventory.SchemaVersion)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(inventory.BaselineCommit) {
		t.Fatalf("baseline_commit = %q", inventory.BaselineCommit)
	}
	if len(inventory.Paths) < 10 {
		t.Fatalf("inventory paths = %d, want at least 10", len(inventory.Paths))
	}

	knownEvents := make(map[string]struct{})
	for _, kind := range runtimeevents.KnownKinds() {
		knownEvents[kind.String()] = struct{}{}
	}
	knownRecords := knownDiagnosticRecordKinds()
	seenPaths := make(map[string]struct{}, len(inventory.Paths))
	for _, path := range inventory.Paths {
		validateCodingDiagnosticPath(t, path, seenPaths, knownEvents, knownRecords)
	}
	for _, pathID := range requiredCodingDiagnosticPathIDs {
		if _, found := seenPaths[pathID]; !found {
			t.Fatalf("inventory does not preserve required path %q", pathID)
		}
	}
}

func validateCodingDiagnosticPath(
	t *testing.T,
	path codingDiagnosticPath,
	seenPaths map[string]struct{},
	knownEvents map[string]struct{},
	knownRecords map[string]struct{},
) {
	t.Helper()
	if !regexp.MustCompile(`^[a-z][a-z0-9_]+$`).MatchString(path.ID) {
		t.Fatalf("invalid path id %q", path.ID)
	}
	if _, duplicate := seenPaths[path.ID]; duplicate {
		t.Fatalf("duplicate path id %q", path.ID)
	}
	seenPaths[path.ID] = struct{}{}
	if path.Support != "supported" || len(path.Authorities) == 0 || len(path.RequiredIDs) == 0 ||
		len(path.AvailableIDs) == 0 || len(path.TerminalFacts) == 0 || path.RetentionOwner == "" {
		t.Fatalf("path %q lacks supported authority, identity, terminal, or retention facts", path.ID)
	}
	if path.ExportClass != "metadata_safe" && path.ExportClass != "private_redacted" {
		t.Fatalf("path %q export_class = %q", path.ID, path.ExportClass)
	}
	assertUniqueNonempty(t, path.ID+" authorities", path.Authorities)
	assertUniqueNonempty(t, path.ID+" runtime_events", path.RuntimeEvents)
	assertUniqueNonempty(t, path.ID+" trace_records", path.TraceRecords)
	assertUniqueNonempty(t, path.ID+" required_ids", path.RequiredIDs)
	assertUniqueNonempty(t, path.ID+" available_ids", path.AvailableIDs)
	assertUniqueNonempty(t, path.ID+" terminal_facts", path.TerminalFacts)
	for _, kind := range path.RuntimeEvents {
		if _, ok := knownEvents[kind]; !ok {
			t.Fatalf("path %q names unknown runtime event %q", path.ID, kind)
		}
	}
	for _, kind := range path.TraceRecords {
		if _, ok := knownRecords[kind]; !ok {
			t.Fatalf("path %q names unknown diagnostic record %q", path.ID, kind)
		}
	}

	gaps := make(map[string]struct{}, len(path.MissingTraceJoins))
	for _, gap := range path.MissingTraceJoins {
		if gap.ID == "" || gap.Observation == "" ||
			(gap.Disposition != "close_h1b" && gap.Disposition != "close_h1c" &&
				gap.Disposition != "external_when_unavailable") {
			t.Fatalf("path %q has invalid gap %#v", path.ID, gap)
		}
		if _, duplicate := gaps[gap.ID]; duplicate {
			t.Fatalf("path %q repeats gap %q", path.ID, gap.ID)
		}
		gaps[gap.ID] = struct{}{}
	}
	for _, identity := range path.RequiredIDs {
		if slices.Contains(path.AvailableIDs, identity) {
			continue
		}
		if _, documented := gaps[identity]; !documented {
			t.Fatalf("path %q requires %q without available evidence or explicit gap", path.ID, identity)
		}
	}
	for identity := range gaps {
		if !slices.Contains(path.RequiredIDs, identity) {
			t.Fatalf("path %q documents non-required gap %q", path.ID, identity)
		}
	}
	if path.ID == "local_coding_turn" || path.ID == "provider_retry_and_fallback" {
		if !slices.Contains(path.AvailableIDs, "model_attempt_id") {
			t.Fatalf("path %q lost model_attempt_id evidence", path.ID)
		}
		if _, unresolved := gaps["model_attempt_id"]; unresolved {
			t.Fatalf("path %q reintroduced the resolved model-attempt gap", path.ID)
		}
	}
	if path.ID == "tool_and_process_lifecycle" {
		if !slices.Contains(path.AvailableIDs, "tool_call_generation") {
			t.Fatalf("path %q lost tool_call_generation evidence", path.ID)
		}
		if _, unresolved := gaps["tool_call_generation"]; unresolved {
			t.Fatalf("path %q reintroduced the resolved tool-call generation gap", path.ID)
		}
	}
	if path.ID == "async_task_and_channel_delivery" {
		for _, identity := range []string{"task_id", "task_generation_id", "completion_id", "delivery_id"} {
			if !slices.Contains(path.AvailableIDs, identity) {
				t.Fatalf("path %q lost %s evidence", path.ID, identity)
			}
			if _, unresolved := gaps[identity]; unresolved {
				t.Fatalf("path %q reintroduced the resolved %s gap", path.ID, identity)
			}
		}
	}
	if path.ID == "human_interaction" {
		for _, identity := range []string{"interaction_id", "interaction_revision", "task_id", "delivery_id"} {
			if !slices.Contains(path.AvailableIDs, identity) {
				t.Fatalf("path %q lost %s evidence", path.ID, identity)
			}
			if _, unresolved := gaps[identity]; unresolved {
				t.Fatalf("path %q reintroduced the resolved %s gap", path.ID, identity)
			}
		}
	}
	if path.ID == "remote_coding_task" {
		for _, identity := range []string{
			"task_id",
			"task_generation_id",
			"worker_generation_id",
			"remote_thread_id",
			"scope_alias",
			"profile_alias",
			"interaction_id",
			"completion_id",
			"delivery_id",
		} {
			if !slices.Contains(path.AvailableIDs, identity) {
				t.Fatalf("path %q lost %s evidence", path.ID, identity)
			}
			if _, unresolved := gaps[identity]; unresolved {
				t.Fatalf("path %q reintroduced the resolved %s gap", path.ID, identity)
			}
		}
	}

	lifecycleGaps := make(map[string]struct{}, len(path.KnownLifecycleGaps))
	for _, gap := range path.KnownLifecycleGaps {
		if gap.ID == "" || gap.Observation == "" ||
			(gap.Disposition != "close_h1b" && gap.Disposition != "close_h1c" &&
				gap.Disposition != "external_when_unavailable") {
			t.Fatalf("path %q has invalid lifecycle gap %#v", path.ID, gap)
		}
		if _, duplicate := lifecycleGaps[gap.ID]; duplicate {
			t.Fatalf("path %q repeats lifecycle gap %q", path.ID, gap.ID)
		}
		lifecycleGaps[gap.ID] = struct{}{}
	}
	if path.ID == "diagnostic_pipeline_health" {
		if _, unresolved := lifecycleGaps["terminal_consistency"]; unresolved {
			t.Fatalf("path %q reintroduced the resolved terminal-consistency gap", path.ID)
		}
	}
}

func assertUniqueNonempty(t *testing.T, label string, values []string) {
	t.Helper()
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("%s contains an empty value", label)
		}
		if _, duplicate := seen[value]; duplicate {
			t.Fatalf("%s repeats %q", label, value)
		}
		seen[value] = struct{}{}
	}
}

func knownDiagnosticRecordKinds() map[string]struct{} {
	kinds := []diagnostictrace.RecordKind{
		diagnostictrace.RecordTurnStart,
		diagnostictrace.RecordTurnEnd,
		diagnostictrace.RecordModelRequest,
		diagnostictrace.RecordModelResponse,
		diagnostictrace.RecordModelRetry,
		diagnostictrace.RecordModelFallbackAttempt,
		diagnostictrace.RecordToolCall,
		diagnostictrace.RecordToolResult,
		diagnostictrace.RecordToolSkipped,
		diagnostictrace.RecordToolLoopDecision,
		diagnostictrace.RecordSteeringInjected,
		diagnostictrace.RecordInterrupt,
		diagnostictrace.RecordDeliveryDecision,
		diagnostictrace.RecordDeliveryAttempt,
		diagnostictrace.RecordDeliveryOutcome,
		diagnostictrace.RecordInteractionLifecycle,
		diagnostictrace.RecordContextCompaction,
		diagnostictrace.RecordContextSnapshot,
		diagnostictrace.RecordSubTurnAdmission,
		diagnostictrace.RecordRuntimeError,
	}
	known := make(map[string]struct{}, len(kinds))
	for _, kind := range kinds {
		known[string(kind)] = struct{}{}
	}
	return known
}
