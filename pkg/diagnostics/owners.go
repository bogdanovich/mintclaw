package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/tasks"
)

const (
	OwnerReportSchemaV1 = "mintclaw.diagnostics.owners.v1"
	HardOwnerIDs        = 64
	MaxOwnerEvents      = 256
)

type OwnerWorkspaceSpec struct {
	ID        string `json:"id"`
	Alias     string `json:"alias,omitempty"`
	Workspace string `json:"-"`
}

type OwnerOptions struct {
	Query              QueryResult
	Workspaces         []OwnerWorkspaceSpec
	CodingRoot         string
	TraceRootsExplicit bool
	Now                func() time.Time
}

type OwnerStoreHealth struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Alias     string `json:"alias,omitempty"`
	RootHash  string `json:"root_hash,omitempty"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
}

type OwnerReport struct {
	SchemaVersion     string                  `json:"schema_version"`
	GeneratedAt       time.Time               `json:"generated_at"`
	Mode              string                  `json:"mode"`
	Availability      string                  `json:"availability"`
	IdentityTruncated bool                    `json:"identity_truncated,omitempty"`
	Stores            []OwnerStoreHealth      `json:"stores,omitempty"`
	Tasks             []TaskInspection        `json:"tasks,omitempty"`
	Interactions      []InteractionInspection `json:"interactions,omitempty"`
	Deliveries        []DeliveryInspection    `json:"deliveries,omitempty"`
	Invocations       []InvocationInspection  `json:"invocations,omitempty"`
	Threads           []thread.Inspection     `json:"threads,omitempty"`
}

type TaskInspection struct {
	StoreID            string                `json:"store_id"`
	TaskID             string                `json:"task_id"`
	GenerationID       string                `json:"generation_id"`
	LastEventSequence  int64                 `json:"last_event_sequence"`
	Runtime            tasks.Runtime         `json:"runtime"`
	ParentTaskID       string                `json:"parent_task_id,omitempty"`
	Status             tasks.Status          `json:"status"`
	DeliveryStatus     tasks.DeliveryStatus  `json:"delivery_status"`
	NotifyPolicy       tasks.NotifyPolicy    `json:"notify_policy"`
	HistoryPolicyKnown bool                  `json:"history_policy_known"`
	HistoryDisabled    bool                  `json:"history_disabled"`
	TimeoutSeconds     int                   `json:"timeout_seconds,omitempty"`
	LastCompletionID   string                `json:"last_completion_id,omitempty"`
	CreatedAt          int64                 `json:"created_at"`
	StartedAt          int64                 `json:"started_at,omitempty"`
	EndedAt            int64                 `json:"ended_at,omitempty"`
	LastEventAt        int64                 `json:"last_event_at,omitempty"`
	CleanupAfter       int64                 `json:"cleanup_after,omitempty"`
	HasDeliverable     bool                  `json:"has_deliverable"`
	Coding             *CodingTaskInspection `json:"coding,omitempty"`
	Events             []TaskEventInspection `json:"events,omitempty"`
	EventsTruncated    bool                  `json:"events_truncated,omitempty"`
}

type CodingTaskInspection struct {
	SchemaVersion      string `json:"schema_version"`
	Alias              string `json:"alias"`
	Target             string `json:"target"`
	Scope              string `json:"scope"`
	Revision           string `json:"revision"`
	Profile            string `json:"profile"`
	OwnerKind          string `json:"owner_kind,omitempty"`
	ThreadID           string `json:"thread_id,omitempty"`
	WorkerGenerationID string `json:"worker_generation_id,omitempty"`
	NodeState          string `json:"node_state,omitempty"`
	NodeRevision       uint64 `json:"node_revision,omitempty"`
	NodeResultDigest   string `json:"node_result_digest,omitempty"`
	Activity           string `json:"activity,omitempty"`
	WorktreeID         string `json:"worktree_id,omitempty"`
	HandoffID          string `json:"handoff_id,omitempty"`
	FailureCode        string `json:"failure_code,omitempty"`
	UpdatedAt          int64  `json:"updated_at,omitempty"`
	RetainUntil        int64  `json:"retain_until,omitempty"`
	QuestionID         string `json:"question_id,omitempty"`
	QuestionRevision   uint64 `json:"question_revision,omitempty"`
	InteractionID      string `json:"interaction_id,omitempty"`
}

type TaskEventInspection struct {
	EventID        string               `json:"event_id"`
	GenerationID   string               `json:"generation_id"`
	Type           tasks.EventType      `json:"type"`
	Status         tasks.Status         `json:"status,omitempty"`
	DeliveryStatus tasks.DeliveryStatus `json:"delivery_status,omitempty"`
	Sequence       int64                `json:"sequence"`
	EmittedAt      int64                `json:"emitted_at"`
}

type InteractionInspection struct {
	StoreID            string                       `json:"store_id"`
	InteractionID      string                       `json:"interaction_id"`
	Kind               interactions.Kind            `json:"kind"`
	Status             interactions.Status          `json:"status"`
	Outcome            interactions.Outcome         `json:"outcome,omitempty"`
	Revision           int64                        `json:"revision"`
	LastEventSequence  int64                        `json:"last_event_sequence"`
	TurnID             string                       `json:"turn_id"`
	ExecutionID        string                       `json:"execution_id,omitempty"`
	ToolCallID         string                       `json:"tool_call_id"`
	ToolName           string                       `json:"tool_name"`
	TaskID             string                       `json:"task_id,omitempty"`
	QuestionCount      int                          `json:"question_count"`
	HasAnswer          bool                         `json:"has_answer"`
	HasProtectedAnswer bool                         `json:"has_protected_answer"`
	CreatedAt          int64                        `json:"created_at"`
	UpdatedAt          int64                        `json:"updated_at"`
	ExpiresAt          int64                        `json:"expires_at"`
	ResolvedAt         int64                        `json:"resolved_at,omitempty"`
	CleanupAfter       int64                        `json:"cleanup_after,omitempty"`
	PromptDeliveryID   string                       `json:"prompt_delivery_id,omitempty"`
	FinalDeliveryIDs   []string                     `json:"final_delivery_ids,omitempty"`
	ResumeTries        int                          `json:"resume_tries,omitempty"`
	LastResumeAt       int64                        `json:"last_resume_at,omitempty"`
	FailureCode        string                       `json:"failure_code,omitempty"`
	Events             []InteractionEventInspection `json:"events,omitempty"`
	EventsTruncated    bool                         `json:"events_truncated,omitempty"`
}

type InteractionEventInspection struct {
	EventID        string                 `json:"event_id"`
	CommitSequence uint64                 `json:"commit_sequence"`
	Type           interactions.EventType `json:"type"`
	From           interactions.Status    `json:"from,omitempty"`
	To             interactions.Status    `json:"to,omitempty"`
	Outcome        interactions.Outcome   `json:"outcome,omitempty"`
	Revision       int64                  `json:"revision"`
	Sequence       int64                  `json:"sequence"`
	EmittedAt      int64                  `json:"emitted_at"`
	Code           string                 `json:"code,omitempty"`
	Success        *bool                  `json:"success,omitempty"`
}

type DeliveryInspection struct {
	StoreID                     string        `json:"store_id"`
	DeliveryID                  string        `json:"delivery_id"`
	Kind                        outbox.Kind   `json:"kind"`
	Status                      outbox.Status `json:"status"`
	Attempts                    int           `json:"attempts"`
	RetryAfter                  time.Time     `json:"retry_after,omitempty"`
	RecoverySettled             bool          `json:"recovery_settled"`
	TranscriptProjected         bool          `json:"transcript_projected"`
	RecoverySettlementPending   bool          `json:"recovery_settlement_pending"`
	TranscriptProjectionPending bool          `json:"transcript_projection_pending"`
	CreatedAt                   time.Time     `json:"created_at"`
	UpdatedAt                   time.Time     `json:"updated_at"`
}

type InvocationInspection struct {
	StoreID string `json:"store_id"`
	nodes.GatewayInvocationInspection
}

type ownerIDs struct {
	tasks        map[string]struct{}
	interactions map[string]struct{}
	deliveries   map[string]struct{}
	invocations  map[string]struct{}
	threads      map[string]struct{}
	truncated    bool
}

// InspectOwners projects authoritative durable state related to the exact
// query and its matched traces. Missing or corrupt owner stores are reported
// with value-free codes and never make trace evidence disappear.
func InspectOwners(options OwnerOptions) (OwnerReport, error) {
	now := time.Now().UTC()
	if options.Now != nil {
		now = options.Now().UTC()
	}
	report := OwnerReport{
		SchemaVersion: OwnerReportSchemaV1,
		GeneratedAt:   now,
		Mode:          "exact_identity",
		Availability:  "available",
	}
	if options.TraceRootsExplicit {
		report.Mode = "trace_only"
		report.Availability = "unavailable_by_design"
		return report, nil
	}
	if len(options.Workspaces) > HardMaxStores {
		return OwnerReport{}, fmt.Errorf("diagnostics owners: workspaces exceed limit %d", HardMaxStores)
	}
	ids := ownerIDsFromQuery(options.Query)
	report.IdentityTruncated = ids.truncated
	seenWorkspaceIDs := make(map[string]struct{}, len(options.Workspaces))
	for _, spec := range options.Workspaces {
		if err := validateOwnerWorkspace(spec); err != nil {
			return OwnerReport{}, err
		}
		if _, duplicate := seenWorkspaceIDs[spec.ID]; duplicate {
			return OwnerReport{}, errors.New("diagnostics owners: duplicate workspace ID")
		}
		seenWorkspaceIDs[spec.ID] = struct{}{}
	}
	if len(ids.tasks) > 0 {
		for _, spec := range options.Workspaces {
			inspectTaskOwners(spec, &ids, &report)
		}
	}
	if len(ids.interactions) > 0 || len(ids.tasks) > 0 {
		for _, spec := range options.Workspaces {
			inspectInteractionOwners(spec, &ids, &report)
		}
	}
	if len(ids.deliveries) > 0 {
		for _, spec := range options.Workspaces {
			inspectDeliveryOwners(spec, &ids, &report)
		}
	}
	if len(ids.invocations) > 0 {
		for _, spec := range options.Workspaces {
			inspectInvocationOwners(spec, &ids, &report)
		}
	}
	inspectCodingThreadOwners(options.CodingRoot, &ids, &report)
	sortOwnerReport(&report)
	if len(report.Stores) == 0 {
		report.Availability = "not_selected"
	}
	return report, nil
}

func ownerIDsFromQuery(query QueryResult) ownerIDs {
	ids := ownerIDs{
		tasks: make(map[string]struct{}), interactions: make(map[string]struct{}),
		deliveries: make(map[string]struct{}), invocations: make(map[string]struct{}),
		threads: make(map[string]struct{}),
	}
	addOwnerID(ids.tasks, query.Selector.TaskID, &ids.truncated)
	addOwnerID(ids.interactions, query.Selector.InteractionID, &ids.truncated)
	addOwnerID(ids.deliveries, query.Selector.DeliveryID, &ids.truncated)
	addOwnerID(ids.invocations, query.Selector.InvocationID, &ids.truncated)
	addThreadOwnerID(ids.threads, query.Selector.ThreadID, &ids.truncated)
	for _, match := range query.Matches {
		addThreadOwnerID(ids.threads, match.Trace.Metadata.RuntimeID, &ids.truncated)
		for _, record := range match.Trace.Records {
			addOwnerID(ids.tasks, record.Scope.TaskID, &ids.truncated)
			addOwnerID(ids.interactions, record.Correlation.InteractionID, &ids.truncated)
			addOwnerID(ids.deliveries, record.Correlation.DeliveryID, &ids.truncated)
			addOwnerID(ids.invocations, record.Correlation.InvocationID, &ids.truncated)
			addThreadOwnerID(ids.threads, record.Correlation.RemoteThreadID, &ids.truncated)
		}
	}
	return ids
}

func addThreadOwnerID(values map[string]struct{}, value string, truncated *bool) {
	value = strings.TrimSpace(value)
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value {
		return
	}
	addOwnerID(values, value, truncated)
}

func addOwnerID(values map[string]struct{}, value string, truncated *bool) {
	value = strings.TrimSpace(value)
	if value == "" || !safeSelectorPattern.MatchString(value) {
		return
	}
	if len(values) >= HardOwnerIDs {
		if _, exists := values[value]; !exists {
			*truncated = true
		}
		return
	}
	values[value] = struct{}{}
}

func validateOwnerWorkspace(spec OwnerWorkspaceSpec) error {
	if !safeSelectorPattern.MatchString(spec.ID) ||
		(spec.Alias != "" && !safeSelectorPattern.MatchString(spec.Alias)) ||
		strings.TrimSpace(spec.Workspace) == "" {
		return errors.New("diagnostics owners: invalid workspace specification")
	}
	return nil
}

func inspectTaskOwners(spec OwnerWorkspaceSpec, ids *ownerIDs, report *OwnerReport) {
	path := tasks.WorkspaceStorePath(spec.Workspace)
	health := ownerHealth(spec, "task_registry", path)
	data, err := readBoundedRegular(path, tasks.DefaultMaxSnapshotBytes)
	if err != nil {
		appendOwnerReadFailure(&report.Stores, health, err)
		return
	}
	snapshot, err := tasks.InspectSnapshot(data)
	if err != nil {
		health.Status, health.ErrorCode = "unavailable", "invalid_snapshot"
		report.Stores = append(report.Stores, health)
		return
	}
	health.Status = "available"
	report.Stores = append(report.Stores, health)
	for _, record := range snapshot.Tasks {
		if _, selected := ids.tasks[record.TaskID]; !selected {
			continue
		}
		inspection := projectTaskInspection(spec.ID, record)
		for _, event := range snapshot.Events {
			if event.TaskID == record.TaskID {
				inspection.Events = append(inspection.Events, projectTaskEvent(event))
			}
		}
		truncateTaskEvents(&inspection)
		report.Tasks = append(report.Tasks, inspection)
		if record.Coding != nil {
			addThreadOwnerID(ids.threads, record.Coding.ThreadID, &ids.truncated)
			if record.Coding.Question != nil {
				addOwnerID(ids.interactions, record.Coding.Question.InteractionID, &ids.truncated)
			}
		}
	}
}

func projectTaskInspection(storeID string, record tasks.Record) TaskInspection {
	inspection := TaskInspection{
		StoreID: storeID, TaskID: ownerToken(record.TaskID), GenerationID: ownerToken(record.GenerationID),
		LastEventSequence: record.LastEventSeq, Runtime: record.Runtime, ParentTaskID: ownerToken(record.ParentTaskID),
		Status: record.Status, DeliveryStatus: record.DeliveryStatus, NotifyPolicy: record.NotifyPolicy,
		HistoryPolicyKnown: record.HistoryPolicyKnown, HistoryDisabled: record.HistoryDisabled,
		TimeoutSeconds: record.TimeoutSeconds, LastCompletionID: ownerToken(record.LastCompletionID),
		CreatedAt: record.CreatedAt, StartedAt: record.StartedAt, EndedAt: record.EndedAt,
		LastEventAt: record.LastEventAt, CleanupAfter: record.CleanupAfter,
		HasDeliverable: record.Deliverable != nil,
	}
	if record.Coding != nil {
		coding := record.Coding
		inspection.Coding = &CodingTaskInspection{
			SchemaVersion: ownerToken(coding.SchemaVersion), Alias: ownerToken(coding.Alias),
			Target: ownerToken(coding.Target), Scope: ownerToken(coding.Scope),
			Revision: ownerToken(coding.Revision), Profile: ownerToken(string(coding.Profile)),
			OwnerKind: ownerToken(coding.OwnerKind), ThreadID: ownerToken(coding.ThreadID),
			WorkerGenerationID: ownerToken(coding.WorkerGenerationID),
			NodeState:          ownerToken(coding.NodeState), NodeRevision: coding.NodeRevision,
			NodeResultDigest: coding.NodeResultDigest, Activity: ownerToken(coding.Activity),
			WorktreeID: ownerToken(coding.WorktreeID), HandoffID: ownerToken(coding.HandoffID),
			FailureCode: ownerToken(coding.FailureCode),
			UpdatedAt:   coding.UpdatedAt, RetainUntil: coding.RetainUntil,
		}
		if coding.Question != nil {
			inspection.Coding.QuestionID = ownerToken(coding.Question.ID)
			inspection.Coding.QuestionRevision = coding.Question.Revision
			inspection.Coding.InteractionID = ownerToken(coding.Question.InteractionID)
		}
	}
	return inspection
}

func projectTaskEvent(event tasks.TaskEvent) TaskEventInspection {
	return TaskEventInspection{
		EventID: ownerToken(event.EventID), GenerationID: ownerToken(event.GenerationID), Type: event.Type,
		Status: event.Status, DeliveryStatus: event.DeliveryStatus, Sequence: event.Seq, EmittedAt: event.EmittedAt,
	}
}

func truncateTaskEvents(inspection *TaskInspection) {
	if len(inspection.Events) <= MaxOwnerEvents {
		return
	}
	inspection.Events = append([]TaskEventInspection(nil), inspection.Events[len(inspection.Events)-MaxOwnerEvents:]...)
	inspection.EventsTruncated = true
}

func inspectInteractionOwners(spec OwnerWorkspaceSpec, ids *ownerIDs, report *OwnerReport) {
	path := interactions.WorkspaceStorePath(spec.Workspace)
	health := ownerHealth(spec, "interaction_registry", path)
	data, err := readBoundedRegular(path, interactions.DefaultMaxBytes)
	if err != nil {
		appendOwnerReadFailure(&report.Stores, health, err)
		return
	}
	snapshot, err := interactions.InspectSnapshot(data)
	if err != nil {
		health.Status, health.ErrorCode = "unavailable", "invalid_snapshot"
		report.Stores = append(report.Stores, health)
		return
	}
	health.Status = "available"
	report.Stores = append(report.Stores, health)
	for _, record := range snapshot.Records {
		_, direct := ids.interactions[record.ID]
		_, byTask := ids.tasks[record.Origin.TaskID]
		if !direct && !byTask {
			continue
		}
		inspection := projectInteractionInspection(spec.ID, record)
		for _, event := range snapshot.Events {
			if event.InteractionID == record.ID {
				inspection.Events = append(inspection.Events, projectInteractionEvent(event))
			}
		}
		if len(inspection.Events) > MaxOwnerEvents {
			inspection.Events = append(
				[]InteractionEventInspection(nil),
				inspection.Events[len(inspection.Events)-MaxOwnerEvents:]...,
			)
			inspection.EventsTruncated = true
		}
		report.Interactions = append(report.Interactions, inspection)
		addOwnerID(ids.interactions, record.ID, &ids.truncated)
		addOwnerID(ids.deliveries, record.PromptDeliveryID, &ids.truncated)
		for _, deliveryID := range record.FinalDeliveryIDs {
			addOwnerID(ids.deliveries, deliveryID, &ids.truncated)
		}
	}
}

func projectInteractionInspection(storeID string, record interactions.Record) InteractionInspection {
	return InteractionInspection{
		StoreID: storeID, InteractionID: ownerToken(record.ID), Kind: record.Kind, Status: record.Status,
		Outcome: record.Outcome, Revision: record.Revision, LastEventSequence: record.LastEventSeq,
		TurnID: ownerToken(record.Origin.TurnID), ExecutionID: ownerToken(record.Origin.ExecutionID),
		ToolCallID: ownerToken(record.Origin.ToolCallID), ToolName: ownerToken(record.Origin.ToolName),
		TaskID:        ownerToken(record.Origin.TaskID),
		QuestionCount: len(record.Questions), HasAnswer: record.Answer != nil,
		HasProtectedAnswer: record.ProtectedAnswer != nil,
		CreatedAt:          record.CreatedAt, UpdatedAt: record.UpdatedAt, ExpiresAt: record.ExpiresAt,
		ResolvedAt: record.ResolvedAt, CleanupAfter: record.CleanupAfter,
		PromptDeliveryID: ownerToken(record.PromptDeliveryID),
		FinalDeliveryIDs: ownerTokens(record.FinalDeliveryIDs),
		ResumeTries:      record.ResumeTries, LastResumeAt: record.LastResumeAt,
		FailureCode: ownerToken(record.FailureCode),
	}
}

func projectInteractionEvent(event interactions.Event) InteractionEventInspection {
	var success *bool
	if event.Success != nil {
		value := *event.Success
		success = &value
	}
	return InteractionEventInspection{
		EventID: ownerToken(event.EventID), CommitSequence: event.CommitSequence, Type: event.Type,
		From: event.From, To: event.To, Outcome: event.Outcome, Revision: event.Revision,
		Sequence: event.Sequence, EmittedAt: event.EmittedAt, Code: ownerToken(event.Code), Success: success,
	}
}

func inspectDeliveryOwners(spec OwnerWorkspaceSpec, ids *ownerIDs, report *OwnerReport) {
	root := outbox.Path(spec.Workspace)
	health := ownerHealth(spec, "outbox", root)
	health.Status = "available"
	foundStore := false
	for _, deliveryID := range sortedOwnerIDs(ids.deliveries) {
		data, err := readBoundedRegular(filepath.Join(root, deliveryID+".json"), outbox.MaxInspectionBytes)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			health.Status, health.ErrorCode = "unavailable", classifyOwnerReadError(err)
			continue
		}
		foundStore = true
		intent, err := outbox.InspectIntent(data, deliveryID)
		if err != nil {
			health.Status, health.ErrorCode = "unavailable", "invalid_record"
			continue
		}
		report.Deliveries = append(report.Deliveries, DeliveryInspection{
			StoreID: spec.ID, DeliveryID: intent.ID, Kind: intent.Identity.Kind, Status: intent.Status,
			Attempts: intent.Attempts, RetryAfter: intent.RetryAfter,
			RecoverySettled: intent.RecoverySettled, TranscriptProjected: intent.TranscriptProjected,
			RecoverySettlementPending:   intent.RecoverySettlementPending(),
			TranscriptProjectionPending: intent.TranscriptProjectionPending(),
			CreatedAt:                   intent.CreatedAt, UpdatedAt: intent.UpdatedAt,
		})
	}
	if !foundStore && health.Status == "available" {
		if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
			health.Status = "not_found"
		}
	}
	report.Stores = append(report.Stores, health)
}

func inspectInvocationOwners(spec OwnerWorkspaceSpec, ids *ownerIDs, report *OwnerReport) {
	path := nodes.GatewayInvocationStorePath(spec.Workspace)
	health := ownerHealth(spec, "gateway_invocations", path)
	health.Status = "available"
	for _, invocationID := range sortedOwnerIDs(ids.invocations) {
		inspection, found, err := nodes.InspectGatewayInvocation(path, invocationID)
		if errors.Is(err, os.ErrNotExist) {
			health.Status = "not_found"
			break
		}
		if err != nil {
			health.Status, health.ErrorCode = "unavailable", "invalid_or_unreadable"
			break
		}
		if found {
			report.Invocations = append(report.Invocations, InvocationInspection{
				StoreID: spec.ID, GatewayInvocationInspection: inspection,
			})
		}
	}
	report.Stores = append(report.Stores, health)
}

func inspectCodingThreadOwners(root string, ids *ownerIDs, report *OwnerReport) {
	if len(ids.threads) == 0 {
		return
	}
	health := OwnerStoreHealth{ID: "coding-threads", Kind: "coding_threads", Status: "available"}
	root = strings.TrimSpace(root)
	if root == "" {
		health.Status = "unavailable"
		health.ErrorCode = "not_configured"
		report.Stores = append(report.Stores, health)
		return
	}
	health.RootHash = pathHash(filepath.Clean(root))
	store, err := thread.NewStore(root)
	if err != nil {
		health.Status, health.ErrorCode = "unavailable", "invalid_root"
		report.Stores = append(report.Stores, health)
		return
	}
	for _, threadID := range sortedOwnerIDs(ids.threads) {
		inspection, err := store.Inspect(context.Background(), threadID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			health.Status = "partial"
			health.ErrorCode = "some_records_unavailable"
			continue
		}
		report.Threads = append(report.Threads, inspection)
	}
	report.Stores = append(report.Stores, health)
}

func ownerHealth(spec OwnerWorkspaceSpec, kind, root string) OwnerStoreHealth {
	return OwnerStoreHealth{
		ID: spec.ID + ":" + kind, Kind: kind, Alias: spec.Alias, RootHash: pathHash(filepath.Clean(root)),
	}
}

func appendOwnerReadFailure(healths *[]OwnerStoreHealth, health OwnerStoreHealth, err error) {
	health.Status = "unavailable"
	health.ErrorCode = classifyOwnerReadError(err)
	if errors.Is(err, os.ErrNotExist) {
		health.Status = "not_found"
	}
	*healths = append(*healths, health)
}

func classifyOwnerReadError(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	case errors.Is(err, os.ErrPermission):
		return "permission_denied"
	default:
		return "invalid_or_unreadable"
	}
}

func readBoundedRegular(path string, maximum int) ([]byte, error) {
	if maximum <= 0 {
		return nil, errors.New("bounded read limit is required")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("diagnostic owner state is not a regular file")
	}
	if before.Size() > int64(maximum) {
		return nil, errors.New("diagnostic owner state exceeds read limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	opened, statErr := file.Stat()
	closeErr := file.Close()
	if err = errors.Join(readErr, statErr, closeErr); err != nil {
		return nil, err
	}
	if len(data) > maximum || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, errors.New("diagnostic owner state changed or exceeded read limit")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() ||
		opened.ModTime() != after.ModTime() {
		return nil, errors.New("diagnostic owner state changed while reading")
	}
	return data, nil
}

func sortedOwnerIDs(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}

func sortOwnerReport(report *OwnerReport) {
	slices.SortFunc(report.Stores, func(a, b OwnerStoreHealth) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(report.Tasks, func(a, b TaskInspection) int {
		if compared := strings.Compare(a.TaskID, b.TaskID); compared != 0 {
			return compared
		}
		return strings.Compare(a.StoreID, b.StoreID)
	})
	slices.SortFunc(report.Interactions, func(a, b InteractionInspection) int {
		if compared := strings.Compare(a.InteractionID, b.InteractionID); compared != 0 {
			return compared
		}
		return strings.Compare(a.StoreID, b.StoreID)
	})
	slices.SortFunc(report.Deliveries, func(a, b DeliveryInspection) int {
		if compared := strings.Compare(a.DeliveryID, b.DeliveryID); compared != 0 {
			return compared
		}
		return strings.Compare(a.StoreID, b.StoreID)
	})
	slices.SortFunc(report.Invocations, func(a, b InvocationInspection) int {
		if compared := strings.Compare(a.InvocationID, b.InvocationID); compared != 0 {
			return compared
		}
		return strings.Compare(a.StoreID, b.StoreID)
	})
	slices.SortFunc(report.Threads, func(a, b thread.Inspection) int {
		return strings.Compare(a.ThreadID, b.ThreadID)
	})
}

func ownerToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !safeSelectorPattern.MatchString(value) {
		return ""
	}
	return value
}

func ownerTokens(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if safe := ownerToken(value); safe != "" {
			result = append(result, safe)
		}
	}
	return result
}
