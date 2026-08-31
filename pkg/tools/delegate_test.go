package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// delegateMockSpawner records the config and returns a canned result.
type delegateMockSpawner struct {
	lastCfg SubTurnConfig
	calls   []SubTurnConfig
	result  *toolshared.ToolResult
	err     error
}

func (m *delegateMockSpawner) SpawnSubTurn(_ context.Context, cfg SubTurnConfig) (*toolshared.ToolResult, error) {
	m.lastCfg = cfg
	m.calls = append(m.calls, cfg)
	if m.err != nil {
		return nil, m.err
	}
	if m.result != nil {
		return m.result, nil
	}
	return &toolshared.ToolResult{
		ForLLM:  "completed: " + cfg.TaskPrompt,
		ForUser: "completed",
	}, nil
}

func newTestDelegateTool(t *testing.T, config DelegateToolConfig) *DelegateTool {
	t.Helper()
	if config.Spawner == nil {
		config.Spawner = &delegateMockSpawner{}
	}
	if config.AllowTarget == nil {
		config.AllowTarget = func(string) bool { return true }
	}
	if config.RequiresObjectiveChecklist == nil {
		config.RequiresObjectiveChecklist = func(string) bool { return false }
	}
	if config.SelfAgentID == "" {
		config.SelfAgentID = "main"
	}
	if config.TaskRegistry == nil {
		config.TaskRegistry = taskregistry.NewRegistry(taskregistry.WorkspaceStorePath(t.TempDir()))
	}
	tool, err := NewDelegateTool(config)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func TestParseObjectiveItemsCarriesStructuredAcceptance(t *testing.T) {
	items, err := parseObjectiveItems([]any{map[string]any{
		"item": "return listings", "kind": "result",
		"acceptance": map[string]any{
			"output_kind": "records", "required_fields": []any{"title", "price", "status"},
			"min_items": float64(3),
		},
	}})
	if err != nil || len(items) != 1 || items[0].Acceptance == nil ||
		items[0].Acceptance.OutputKind != "records" || items[0].Acceptance.MinItems != 3 ||
		len(items[0].Acceptance.RequiredFields) != 3 {
		t.Fatalf("parseObjectiveItems() = (%#v, %v)", items, err)
	}
}

func TestParseObjectiveItemsRejectsInvalidAcceptance(t *testing.T) {
	tests := []map[string]any{
		{
			"item": "publish", "kind": "external_action",
			"acceptance": map[string]any{"output_kind": "text"},
		},
		{
			"item": "report", "kind": "result",
			"acceptance": map[string]any{"output_kind": "text", "required_fields": []any{"title"}},
		},
		{
			"item": "report", "kind": "result",
			"acceptance": map[string]any{"output_kind": "records", "min_items": float64(1.5)},
		},
		{
			"item": "report", "kind": "result",
			"acceptance": map[string]any{"output_kind": "records", "min_item": float64(3)},
		},
	}
	for _, objective := range tests {
		if items, err := parseObjectiveItems([]any{objective}); err == nil {
			t.Fatalf("invalid acceptance was parsed: %#v", items)
		}
	}
}

func TestDelegateTool_Name(t *testing.T) {
	tool := newTestDelegateTool(t, DelegateToolConfig{})
	if tool.Name() != "delegate" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "delegate")
	}
}

func TestDelegateTool_Parameters(t *testing.T) {
	tool := newTestDelegateTool(t, DelegateToolConfig{})
	params := tool.Parameters()

	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties should be a map")
	}
	objectiveItems := props["objective_items"].(map[string]any)
	itemSchema := objectiveItems["items"].(map[string]any)
	itemProperties := itemSchema["properties"].(map[string]any)
	acceptance := itemProperties["acceptance"].(map[string]any)
	if additional, ok := acceptance["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("acceptance additionalProperties = %#v, want false", acceptance["additionalProperties"])
	}
	_, hasAgentID := props["agent_id"]
	if !hasAgentID {
		t.Error("agent_id parameter should exist")
	}
	_, hasTask := props["task"]
	if !hasTask {
		t.Error("task parameter should exist")
	}

	required, ok := params["required"].([]string)
	if !ok {
		t.Fatal("required should be a string array")
	}
	if len(required) != 2 {
		t.Fatalf("required should have 2 entries, got %d", len(required))
	}
}

func TestDelegateTool_BrowserObjectivePreflightRejectsBeforeSpawning(t *testing.T) {
	spawner := &delegateMockSpawner{}
	tool := newTestDelegateTool(t, DelegateToolConfig{
		Spawner: spawner,
		RequiresObjectiveChecklist: func(targetAgentID string) bool {
			return targetAgentID == "browser"
		},
	})

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "browser",
		"task":     "inspect two listings",
	})

	if result == nil || !result.IsError {
		t.Fatalf("result = %#v, want error", result)
	}
	if !strings.Contains(result.ForLLM, "retry delegate") {
		t.Fatalf("ForLLM = %q, want retry guidance", result.ForLLM)
	}
	if len(spawner.calls) != 0 {
		t.Fatalf("spawner calls = %#v, want none", spawner.calls)
	}
}

func TestDelegateTool_Execute_Success(t *testing.T) {
	spawner := &delegateMockSpawner{}
	tool := newTestDelegateTool(t, DelegateToolConfig{Spawner: spawner})

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "researcher",
		"task":     "summarize the logs",
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, `[Response from agent "researcher"]`) {
		t.Errorf("result should contain attribution, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "summarize the logs") {
		t.Errorf("result should contain task output, got: %s", result.ForLLM)
	}

	// Verify spawner received correct config
	if spawner.lastCfg.TargetAgentID != "researcher" {
		t.Errorf("TargetAgentID = %q, want %q", spawner.lastCfg.TargetAgentID, "researcher")
	}
	if spawner.lastCfg.Async {
		t.Error("delegate should be synchronous (Async=false)")
	}
	if spawner.lastCfg.TaskPrompt != "summarize the logs" {
		t.Errorf("TaskPrompt = %q, want %q", spawner.lastCfg.TaskPrompt, "summarize the logs")
	}
	if spawner.lastCfg.DeliveryMode != toolshared.AsyncDeliveryParentOnly {
		t.Errorf("DeliveryMode = %q, want %q", spawner.lastCfg.DeliveryMode, toolshared.AsyncDeliveryParentOnly)
	}
}

func TestDelegateTool_Execute_PreservesDurableChildSuspension(t *testing.T) {
	spawner := &delegateMockSpawner{result: &toolshared.ToolResult{
		Control: toolshared.ToolControl{TaskSuspended: true},
	}}
	tool := newTestDelegateTool(t, DelegateToolConfig{Spawner: spawner})

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "specialist",
		"task":     "commit the external action after durable approval",
	})

	if !result.Control.TaskSuspended || !result.Delivery.IsFinalHandled() || result.ForUser != "" {
		t.Fatalf("suspended delegate result = %#v", result)
	}
	for _, required := range []string{
		"already delivered the exact approval prompt",
		"Do not ask another confirmation",
		"invent missing credentials or human steps",
		"start a replacement delegate",
	} {
		if !strings.Contains(result.ForLLM, required) {
			t.Fatalf("suspension guidance omitted %q: %q", required, result.ForLLM)
		}
	}
}

func TestDelegateTool_Execute_PassesTimeoutSeconds(t *testing.T) {
	spawner := &delegateMockSpawner{}
	tool := newTestDelegateTool(t, DelegateToolConfig{Spawner: spawner})

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id":        "researcher",
		"task":            "summarize the logs",
		"timeout_seconds": 2.5,
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	if spawner.lastCfg.Timeout != 2500*time.Millisecond {
		t.Fatalf("Timeout = %v, want 2.5s", spawner.lastCfg.Timeout)
	}
}

func TestDelegateTool_Execute_RecordsTaskRegistry(t *testing.T) {
	registry := taskregistry.NewRegistry(taskregistry.WorkspaceStorePath(t.TempDir()))
	spawner := &delegateMockSpawner{}
	tool := newTestDelegateTool(t, DelegateToolConfig{Spawner: spawner, TaskRegistry: registry})

	ctx := toolshared.WithToolContext(context.Background(), "telegram", "chat-1")
	ctx = toolshared.WithToolTopicID(ctx, "topic-1")
	ctx = toolshared.WithToolSessionContext(ctx, "main", "session-1", nil)
	result := tool.Execute(ctx, map[string]any{
		"agent_id": "media",
		"task":     "download reel",
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	records := registry.List()
	if len(records) != 1 {
		t.Fatalf("registry records = %d, want 1: %#v", len(records), records)
	}
	rec := records[0]
	if !strings.HasPrefix(rec.TaskID, "delegate-") {
		t.Fatalf("TaskID = %q, want delegate-*", rec.TaskID)
	}
	if spawner.lastCfg.TaskID != rec.TaskID {
		t.Fatalf("subturn TaskID = %q, want %q", spawner.lastCfg.TaskID, rec.TaskID)
	}
	if rec.Runtime != taskregistry.RuntimeDelegate {
		t.Fatalf("Runtime = %q, want %q", rec.Runtime, taskregistry.RuntimeDelegate)
	}
	if rec.TaskKind != "delegate" {
		t.Fatalf("TaskKind = %q, want delegate", rec.TaskKind)
	}
	if rec.Status != taskregistry.StatusSucceeded {
		t.Fatalf("Status = %q, want succeeded", rec.Status)
	}
	if rec.DeliveryStatus != taskregistry.DeliverySessionQueued {
		t.Fatalf("DeliveryStatus = %q, want session_queued", rec.DeliveryStatus)
	}
	if rec.AgentID != "media" || rec.Channel != "telegram" || rec.ChatID != "chat-1" || rec.TopicID != "topic-1" {
		t.Fatalf("unexpected routing fields: %+v", rec)
	}
	if rec.RequesterSessionKey != "session-1" || rec.OwnerKey != "main" {
		t.Fatalf("unexpected owner fields: %+v", rec)
	}
	if !rec.HistoryPolicyKnown {
		t.Fatalf("history policy provenance was not persisted: %+v", rec)
	}
	if rec.Deliverable != nil {
		t.Fatalf("unexpected deliverable for plain result: %+v", rec.Deliverable)
	}
}

func TestDelegateTool_Execute_RecordsDeliverable(t *testing.T) {
	registry := taskregistry.NewRegistry(taskregistry.WorkspaceStorePath(t.TempDir()))
	spawner := &delegateMockSpawner{
		result: (&toolshared.ToolResult{
			ForLLM: "child finished",
			Deliverable: &taskresult.Deliverable{
				Text: "recipe text",
				Artifacts: []taskresult.Artifact{{
					Ref:         "media://video",
					Kind:        "video",
					Filename:    "source.mp4",
					ContentType: "video/mp4",
				}},
			},
		}),
	}
	tool := newTestDelegateTool(t, DelegateToolConfig{Spawner: spawner, TaskRegistry: registry})

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "media",
		"task":     "download reel",
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	records := registry.List()
	if len(records) != 1 {
		t.Fatalf("registry records = %d, want 1: %#v", len(records), records)
	}
	rec := records[0]
	if rec.Deliverable == nil || rec.Deliverable.Text != "recipe text" {
		t.Fatalf("Deliverable = %+v, want recipe text", rec.Deliverable)
	}
	if len(rec.Deliverable.Artifacts) != 1 || rec.Deliverable.Artifacts[0].Ref != "media://video" {
		t.Fatalf("Deliverable artifacts = %+v, want media://video", rec.Deliverable.Artifacts)
	}
}

func TestDelegateTool_Execute_RecordsBlockedObjectiveAsFailed(t *testing.T) {
	registry := taskregistry.NewRegistry(taskregistry.WorkspaceStorePath(t.TempDir()))
	spawner := &delegateMockSpawner{result: (&toolshared.ToolResult{
		ForLLM: "browser objective blocked",
		Deliverable: &taskresult.Deliverable{ObjectiveOutcome: &taskresult.Outcome{
			Status:       taskresult.OutcomeBlocked,
			MissingItems: []string{"Craigslist verification"},
		}},
	})}
	tool := newTestDelegateTool(t, DelegateToolConfig{Spawner: spawner, TaskRegistry: registry})

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "browser",
		"task":     "verify Craigslist listings",
	})
	if result.IsError {
		t.Fatalf("structured blocked result should still be delivered: %s", result.ForLLM)
	}
	records := registry.List()
	if len(records) != 1 || records[0].Status != taskregistry.StatusFailed ||
		records[0].Deliverable == nil || records[0].Deliverable.ObjectiveOutcome == nil ||
		records[0].Deliverable.ObjectiveOutcome.Status != taskresult.OutcomeBlocked {
		t.Fatalf("delegate task record = %#v", records)
	}
}

func TestDelegateTool_Execute_RecordsExplicitDeliverableReport(t *testing.T) {
	registry := taskregistry.NewRegistry(taskregistry.WorkspaceStorePath(t.TempDir()))
	spawner := &delegateMockSpawner{
		result: (&toolshared.ToolResult{
			ForLLM: "review finished",
		}).WithDeliverable(&taskresult.Deliverable{
			Text: "No issues found",
			Report: &taskresult.Report{
				SchemaVersion: taskresult.ReportSchemaV1,
				ReportID:      "review-1",
				ContentHash:   "abc123",
				Summary:       "No high-confidence issues found",
				Claims: []taskresult.Claim{{
					Kind:       "negative_evidence",
					Text:       "No correctness issues found",
					Confidence: "high",
				}},
				FieldDeltas: []taskresult.FieldDelta{{
					Field: "review_status",
					From:  "pending",
					To:    "clean",
				}},
				Provenance: map[string]string{"producer": "reviewer"},
			},
		}),
	}
	tool := newTestDelegateTool(t, DelegateToolConfig{Spawner: spawner, TaskRegistry: registry})

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "reviewer",
		"task":     "review PR",
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	records := registry.List()
	if len(records) != 1 {
		t.Fatalf("registry records = %d, want 1: %#v", len(records), records)
	}
	report := records[0].Deliverable.Report
	if report == nil {
		t.Fatal("expected explicit deliverable report")
	}
	if report.ReportID != "review-1" || report.ContentHash != "abc123" {
		t.Fatalf("report identity = %+v", report)
	}
	if len(report.Claims) != 1 || report.Claims[0].Kind != "negative_evidence" {
		t.Fatalf("report claims = %+v", report.Claims)
	}
	if len(report.FieldDeltas) != 1 || report.FieldDeltas[0].To != "clean" {
		t.Fatalf("field deltas = %+v", report.FieldDeltas)
	}
	if report.Provenance["producer"] != "reviewer" {
		t.Fatalf("provenance = %+v", report.Provenance)
	}
}

func TestDelegateTool_Execute_RecordsExplicitDeliverableArtifact(t *testing.T) {
	registry := taskregistry.NewRegistry(taskregistry.WorkspaceStorePath(t.TempDir()))
	spawner := &delegateMockSpawner{
		result: (&toolshared.ToolResult{
			ForLLM: "child finished",
			Deliverable: &taskresult.Deliverable{
				Text: "recipe",
				Artifacts: []taskresult.Artifact{{
					Ref: "file:/tmp/mintclaw/source.mp4", LocalPath: "/tmp/mintclaw/source.mp4",
					Kind: "video", Filename: "source.mp4", ContentType: "video/mp4",
				}},
			},
		}),
	}
	tool := newTestDelegateTool(t, DelegateToolConfig{Spawner: spawner, TaskRegistry: registry})

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "media",
		"task":     "download reel",
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	records := registry.List()
	if len(records) != 1 {
		t.Fatalf("registry records = %d, want 1: %#v", len(records), records)
	}
	deliverable := records[0].Deliverable
	if deliverable == nil {
		t.Fatal("expected deliverable")
	}
	if len(deliverable.Artifacts) != 1 {
		t.Fatalf("artifact count = %d, want 1: %+v", len(deliverable.Artifacts), deliverable)
	}
	artifact := deliverable.Artifacts[0]
	if artifact.Ref != "file:/tmp/mintclaw/source.mp4" {
		t.Fatalf("artifact ref = %q, want file:/tmp/mintclaw/source.mp4", artifact.Ref)
	}
	if artifact.Kind != "video" {
		t.Fatalf("artifact kind = %q, want video", artifact.Kind)
	}
	if artifact.Filename != "source.mp4" {
		t.Fatalf("artifact filename = %q, want source.mp4", artifact.Filename)
	}
	if artifact.ContentType != "video/mp4" {
		t.Fatalf("artifact content type = %q, want video/mp4", artifact.ContentType)
	}
}

func TestDelegateTool_Execute_EmptyAgentID(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{"missing", map[string]any{"task": "test"}},
		{"empty string", map[string]any{"agent_id": "", "task": "test"}},
		{"whitespace only", map[string]any{"agent_id": "  ", "task": "test"}},
		{"wrong type", map[string]any{"agent_id": 123, "task": "test"}},
	}

	tool := newTestDelegateTool(t, DelegateToolConfig{})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tool.Execute(context.Background(), tt.args)
			if !result.IsError {
				t.Error("expected error for invalid agent_id")
			}
			if !strings.Contains(result.ForLLM, "agent_id is required") {
				t.Errorf("error should mention agent_id, got: %s", result.ForLLM)
			}
		})
	}
}

func TestDelegateTool_Execute_EmptyTask(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{"missing", map[string]any{"agent_id": "a"}},
		{"empty string", map[string]any{"agent_id": "a", "task": ""}},
		{"whitespace only", map[string]any{"agent_id": "a", "task": "\t\n"}},
	}

	tool := newTestDelegateTool(t, DelegateToolConfig{})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tool.Execute(context.Background(), tt.args)
			if !result.IsError {
				t.Error("expected error for invalid task")
			}
			if !strings.Contains(result.ForLLM, "task is required") {
				t.Errorf("error should mention task, got: %s", result.ForLLM)
			}
		})
	}
}

func TestDelegateTool_Execute_PermissionDenied(t *testing.T) {
	tool := newTestDelegateTool(t, DelegateToolConfig{
		AllowTarget: func(targetAgentID string) bool {
			return targetAgentID == "allowed-agent"
		},
	})

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "forbidden-agent",
		"task":     "test",
	})

	if !result.IsError {
		t.Error("expected error for denied agent")
	}
	if !strings.Contains(result.ForLLM, "not allowed to delegate") {
		t.Errorf("error should mention permission, got: %s", result.ForLLM)
	}
}

func TestDelegateTool_Execute_PermissionAllowed(t *testing.T) {
	tool := newTestDelegateTool(t, DelegateToolConfig{
		AllowTarget: func(targetAgentID string) bool {
			return targetAgentID == "allowed-agent"
		},
	})

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "allowed-agent",
		"task":     "test",
	})

	if result.IsError {
		t.Errorf("expected success for allowed agent, got error: %s", result.ForLLM)
	}
}

func TestNewDelegateTool_RequiresDependencies(t *testing.T) {
	registry := taskregistry.NewRegistry(taskregistry.WorkspaceStorePath(t.TempDir()))
	spawner := &delegateMockSpawner{}
	allowTarget := func(string) bool { return true }
	objectivePolicy := func(string) bool { return false }
	tests := []struct {
		name   string
		config DelegateToolConfig
		want   string
	}{
		{name: "child runner", config: DelegateToolConfig{
			AllowTarget: allowTarget, RequiresObjectiveChecklist: objectivePolicy,
			SelfAgentID: "main", TaskRegistry: registry,
		}, want: "child runner is required"},
		{name: "task registry", config: DelegateToolConfig{
			Spawner: spawner, AllowTarget: allowTarget,
			RequiresObjectiveChecklist: objectivePolicy, SelfAgentID: "main",
		}, want: "task registry is required"},
		{name: "allow-list", config: DelegateToolConfig{
			Spawner: spawner, RequiresObjectiveChecklist: objectivePolicy,
			SelfAgentID: "main", TaskRegistry: registry,
		}, want: "allow-list policy is required"},
		{name: "objective", config: DelegateToolConfig{
			Spawner: spawner, AllowTarget: allowTarget,
			SelfAgentID: "main", TaskRegistry: registry,
		}, want: "objective policy is required"},
		{name: "self identity", config: DelegateToolConfig{
			Spawner: spawner, AllowTarget: allowTarget,
			RequiresObjectiveChecklist: objectivePolicy, TaskRegistry: registry,
		}, want: "self agent ID is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tool, err := NewDelegateTool(test.config)
			if tool != nil || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewDelegateTool() = (%#v, %v), want %q", tool, err, test.want)
			}
		})
	}
}

func TestDelegateTool_Execute_SpawnerError(t *testing.T) {
	spawner := &delegateMockSpawner{
		err: fmt.Errorf("context deadline exceeded"),
	}
	tool := newTestDelegateTool(t, DelegateToolConfig{Spawner: spawner})

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "researcher",
		"task":     "test",
	})

	if !result.IsError {
		t.Error("expected error when spawner fails")
	}
	if !strings.Contains(result.ForLLM, "delegation to agent") {
		t.Errorf("error should mention delegation failure, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "context deadline exceeded") {
		t.Errorf("error should propagate cause, got: %s", result.ForLLM)
	}
}

func TestDelegateTool_Execute_AllowAllPolicy(t *testing.T) {
	tool := newTestDelegateTool(t, DelegateToolConfig{AllowTarget: func(string) bool { return true }})

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "any-agent",
		"task":     "test",
	})

	if result.IsError {
		t.Errorf("expected success from allow-all policy, got error: %s", result.ForLLM)
	}
}

func TestDelegateTool_Execute_UserOnlyMarksHandled(t *testing.T) {
	spawner := &delegateMockSpawner{}
	tool := newTestDelegateTool(t, DelegateToolConfig{Spawner: spawner})

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id":      "media",
		"task":          "deliver this to the user",
		"delivery_mode": string(toolshared.AsyncDeliveryUserOnly),
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	if !result.Delivery.IsFinalHandled() {
		t.Fatal("expected delegate user_only result to own final delivery")
	}
	if !result.Delivery.SuppressesImplicitUserOutput() {
		t.Fatal("expected delegate user_only result to suppress implicit parent delivery")
	}
	if spawner.lastCfg.DeliveryMode != toolshared.AsyncDeliveryUserOnly {
		t.Fatalf("DeliveryMode = %q, want %q", spawner.lastCfg.DeliveryMode, toolshared.AsyncDeliveryUserOnly)
	}
}

func TestDelegateTool_Execute_InvalidDeliveryMode(t *testing.T) {
	tool := newTestDelegateTool(t, DelegateToolConfig{})

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id":      "media",
		"task":          "test",
		"delivery_mode": "wrong",
	})

	if !result.IsError {
		t.Fatal("expected invalid delivery_mode to error")
	}
	if !strings.Contains(result.ForLLM, "delivery_mode") {
		t.Fatalf("expected delivery_mode error, got %q", result.ForLLM)
	}
}

func TestDelegateTool_Execute_NilResult(t *testing.T) {
	tool := newTestDelegateTool(t, DelegateToolConfig{Spawner: &nilResultSpawner{}})

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "researcher",
		"task":     "test",
	})

	if !result.IsError {
		t.Error("expected error for nil result")
	}
	if !strings.Contains(result.ForLLM, "returned no result") {
		t.Errorf("error should mention no result, got: %s", result.ForLLM)
	}
}

func TestDelegateTool_Execute_SelfDelegation(t *testing.T) {
	tool := newTestDelegateTool(t, DelegateToolConfig{SelfAgentID: "alpha"})

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "alpha",
		"task":     "test",
	})

	if !result.IsError {
		t.Error("expected error for self-delegation")
	}
	if !strings.Contains(result.ForLLM, "cannot delegate to self") {
		t.Errorf("error should mention self-delegation, got: %s", result.ForLLM)
	}
}

func TestDelegateTool_Execute_SelfDelegation_Normalized(t *testing.T) {
	tool := newTestDelegateTool(t, DelegateToolConfig{SelfAgentID: " Alpha "})

	// Case-insensitive and whitespace variants should still be caught
	variants := []string{"ALPHA", " Alpha ", "  alpha  "}
	for _, v := range variants {
		t.Run(v, func(t *testing.T) {
			result := tool.Execute(context.Background(), map[string]any{
				"agent_id": v,
				"task":     "test",
			})
			if !result.IsError {
				t.Errorf("agent_id=%q should be caught as self-delegation", v)
			}
		})
	}
}

// nilResultSpawner always returns (nil, nil).
type nilResultSpawner struct{}

func (m *nilResultSpawner) SpawnSubTurn(_ context.Context, _ SubTurnConfig) (*toolshared.ToolResult, error) {
	return nil, nil
}
