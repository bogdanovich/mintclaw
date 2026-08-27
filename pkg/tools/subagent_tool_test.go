package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestSubagentManager_AppliesLLMDefaultsToChildRunner(t *testing.T) {
	spawner := &mockSpawner{}
	manager, err := NewSubagentManager(SubagentManagerConfig{
		DefaultModel: "test-model",
		MaxTokens:    2048,
		Temperature:  0.6,
		Spawner:      spawner,
		TaskRegistry: taskregistry.NewRegistry(taskregistry.WorkspaceStorePath(t.TempDir())),
	})
	if err != nil {
		t.Fatal(err)
	}

	result := newTestSubagentTool(t, manager).Execute(context.Background(), map[string]any{"task": "inspect"})
	if result == nil || result.IsError {
		t.Fatalf("subagent result = %#v", result)
	}
	if spawner.lastConfig.Model != "test-model" || spawner.lastConfig.MaxTokens != 2048 ||
		spawner.lastConfig.Temperature != 0.6 {
		t.Fatalf("child runner config = %#v", spawner.lastConfig)
	}
}

func newTestSubagentTool(t *testing.T, manager *SubagentManager) *SubagentTool {
	t.Helper()
	tool, err := NewSubagentTool(manager)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

// TestSubagentTool_Name verifies tool name
func TestSubagentTool_Name(t *testing.T) {
	manager := newTestSubagentManager(t, "test-model", t.TempDir())
	tool := newTestSubagentTool(t, manager)

	if tool.Name() != "subagent" {
		t.Errorf("Expected name 'subagent', got '%s'", tool.Name())
	}
}

// TestSubagentTool_Description verifies tool description
func TestSubagentTool_Description(t *testing.T) {
	manager := newTestSubagentManager(t, "test-model", t.TempDir())
	tool := newTestSubagentTool(t, manager)

	desc := tool.Description()
	if desc == "" {
		t.Error("Description should not be empty")
	}
	if !strings.Contains(desc, "subagent") {
		t.Errorf("Description should mention 'subagent', got: %s", desc)
	}
}

// TestSubagentTool_Parameters verifies tool parameters schema
func TestSubagentTool_Parameters(t *testing.T) {
	manager := newTestSubagentManager(t, "test-model", t.TempDir())
	tool := newTestSubagentTool(t, manager)

	params := tool.Parameters()
	if params == nil {
		t.Error("Parameters should not be nil")
	}

	// Check type
	if params["type"] != "object" {
		t.Errorf("Expected type 'object', got: %v", params["type"])
	}

	// Check properties
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("Properties should be a map")
	}

	// Verify task parameter
	task, ok := props["task"].(map[string]any)
	if !ok {
		t.Fatal("Task parameter should exist")
	}
	if task["type"] != "string" {
		t.Errorf("Task type should be 'string', got: %v", task["type"])
	}

	// Verify label parameter
	label, ok := props["label"].(map[string]any)
	if !ok {
		t.Fatal("Label parameter should exist")
	}
	if label["type"] != "string" {
		t.Errorf("Label type should be 'string', got: %v", label["type"])
	}

	// Check required fields
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatal("Required should be a string array")
	}
	if len(required) != 1 || required[0] != "task" {
		t.Errorf("Required should be ['task'], got: %v", required)
	}
}

// TestSubagentTool_Execute_Success tests successful execution
func TestSubagentTool_Execute_Success(t *testing.T) {
	manager := newTestSubagentManager(t, "test-model", t.TempDir())
	tool := newTestSubagentTool(t, manager)

	ctx := toolshared.WithToolContext(context.Background(), "telegram", "chat-123")
	args := map[string]any{
		"task":  "Write a haiku about coding",
		"label": "haiku-task",
	}

	result := tool.Execute(ctx, args)

	// Verify basic ToolResult structure
	if result == nil {
		t.Fatal("Result should not be nil")
	}

	// Verify no error
	if result.IsError {
		t.Errorf("Expected success, got error: %s", result.ForLLM)
	}

	// Verify not async
	if result.Control.Async {
		t.Error("SubagentTool should be synchronous, not async")
	}

	// Verify not silent
	if result.Delivery.IsSilent() {
		t.Error("SubagentTool should not be silent")
	}

	// Verify ForUser contains brief summary (not empty)
	if result.ForUser == "" {
		t.Error("ForUser should contain result summary")
	}
	if !strings.Contains(result.ForUser, "Task completed") {
		t.Errorf("ForUser should contain task completion, got: %s", result.ForUser)
	}

	// Verify ForLLM contains full details
	if result.ForLLM == "" {
		t.Error("ForLLM should contain full details")
	}
	if !strings.Contains(result.ForLLM, "haiku-task") {
		t.Errorf("ForLLM should contain label 'haiku-task', got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "Task completed:") {
		t.Errorf("ForLLM should contain task result, got: %s", result.ForLLM)
	}
}

func TestSubagentToolPreservesStructuredSpawnResult(t *testing.T) {
	want := (&toolshared.ToolResult{ForLLM: "verified", ForUser: "verified"}).
		WithWriteAudit(toolshared.WriteAuditEntry{Target: "https://example.com", Success: true}).
		WithDeliverable(&taskresult.Deliverable{ObjectiveOutcome: &taskresult.Outcome{
			Status: taskresult.OutcomePartial, MissingItems: []string{"second item"},
		}})
	manager := newTestSubagentManager(t, "test-model", t.TempDir(), &delegateMockSpawner{result: want})
	tool := newTestSubagentTool(t, manager)

	got := tool.Execute(context.Background(), map[string]any{"task": "publish items"})
	if got != want || len(got.WriteAudit) != 1 || got.Deliverable == nil ||
		got.Deliverable.ObjectiveOutcome == nil ||
		got.Deliverable.ObjectiveOutcome.Status != taskresult.OutcomePartial {
		t.Fatalf("structured spawn result was lost: %#v", got)
	}
}

// TestSubagentTool_Execute_NoLabel tests execution without label
func TestSubagentTool_Execute_NoLabel(t *testing.T) {
	manager := newTestSubagentManager(t, "test-model", t.TempDir())
	tool := newTestSubagentTool(t, manager)

	ctx := context.Background()
	args := map[string]any{
		"task": "Test task without label",
	}

	result := tool.Execute(ctx, args)

	if result.IsError {
		t.Errorf("Expected success without label, got error: %s", result.ForLLM)
	}

	// ForLLM should show (unnamed) for missing label
	if !strings.Contains(result.ForLLM, "(unnamed)") {
		t.Errorf("ForLLM should show '(unnamed)' for missing label, got: %s", result.ForLLM)
	}
}

// TestSubagentTool_Execute_MissingTask tests error handling for missing task
func TestSubagentTool_Execute_MissingTask(t *testing.T) {
	manager := newTestSubagentManager(t, "test-model", t.TempDir())
	tool := newTestSubagentTool(t, manager)

	ctx := context.Background()
	args := map[string]any{
		"label": "test",
	}

	result := tool.Execute(ctx, args)

	// Should return error
	if !result.IsError {
		t.Error("Expected error for missing task parameter")
	}

	// ForLLM should contain error message
	if !strings.Contains(result.ForLLM, "task is required") {
		t.Errorf("Error message should mention 'task is required', got: %s", result.ForLLM)
	}

	// Err should be set
	if result.Err == nil {
		t.Error("Err should be set for validation failure")
	}
}

func TestNewSubagentTool_RequiresManager(t *testing.T) {
	tool, err := NewSubagentTool(nil)
	if tool != nil || err == nil || !strings.Contains(err.Error(), "manager is required") {
		t.Fatalf("NewSubagentTool(nil) = (%#v, %v)", tool, err)
	}
}

func TestNewSubagentManager_RequiresDependencies(t *testing.T) {
	registry := taskregistry.NewRegistry(taskregistry.WorkspaceStorePath(t.TempDir()))
	tests := []struct {
		name   string
		config SubagentManagerConfig
		want   string
	}{
		{name: "child runner", config: SubagentManagerConfig{
			DefaultModel: "test-model", TaskRegistry: registry,
		}, want: "child runner is required"},
		{name: "task registry", config: SubagentManagerConfig{
			DefaultModel: "test-model", Spawner: &mockSpawner{},
		}, want: "task registry is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, err := NewSubagentManager(test.config)
			if manager != nil || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewSubagentManager() = (%#v, %v), want %q", manager, err, test.want)
			}
		})
	}
}

// TestSubagentTool_Execute_ContextPassing verifies context is properly used
func TestSubagentTool_Execute_ContextPassing(t *testing.T) {
	manager := newTestSubagentManager(t, "test-model", t.TempDir())
	tool := newTestSubagentTool(t, manager)

	channel := "test-channel"
	chatID := "test-chat"
	ctx := toolshared.WithToolContext(context.Background(), channel, chatID)
	args := map[string]any{
		"task": "Test context passing",
	}

	result := tool.Execute(ctx, args)

	// Should succeed
	if result.IsError {
		t.Errorf("Expected success with context, got error: %s", result.ForLLM)
	}

	// The context is used internally; we can't directly test it
	// but execution success indicates context was handled properly
}

// TestSubagentTool_ForUserTruncation verifies long content is truncated for user
func TestSubagentTool_ForUserTruncation(t *testing.T) {
	// Create a mock provider that returns very long content
	manager := newTestSubagentManager(t, "test-model", t.TempDir())
	tool := newTestSubagentTool(t, manager)

	ctx := context.Background()

	// Create a task that will generate long response
	longTask := strings.Repeat("This is a very long task description. ", 100)
	args := map[string]any{
		"task":  longTask,
		"label": "long-test",
	}

	result := tool.Execute(ctx, args)

	// ForUser should be truncated to 500 chars + "..."
	maxUserLen := 500
	if len(result.ForUser) > maxUserLen+3 { // +3 for "..."
		t.Errorf("ForUser should be truncated to ~%d chars, got: %d", maxUserLen, len(result.ForUser))
	}

	// ForLLM should have full content
	if !strings.Contains(result.ForLLM, longTask[:50]) {
		t.Error("ForLLM should contain reference to original task")
	}
}
