package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// mockSpawner implements SubTurnSpawner for testing.
type mockSpawner struct {
	lastConfig SubTurnConfig
	done       chan struct{}
}

func (m *mockSpawner) SpawnSubTurn(ctx context.Context, cfg SubTurnConfig) (*toolshared.ToolResult, error) {
	m.lastConfig = cfg
	if m.done != nil {
		close(m.done)
	}

	// Extract task from system prompt for response
	task := cfg.SystemPrompt
	if strings.Contains(task, "Task: ") {
		parts := strings.Split(task, "Task: ")
		if len(parts) > 1 {
			task = parts[1]
		}
	}
	return &toolshared.ToolResult{
		ForLLM:  "Task completed: " + task,
		ForUser: "Task completed",
	}, nil
}

func TestSpawnTool_Execute_EmptyTask(t *testing.T) {
	manager := NewSubagentManager("test-model", t.TempDir())
	tool := NewSpawnTool(manager)

	ctx := context.Background()

	tests := []struct {
		name string
		args map[string]any
	}{
		{"empty string", map[string]any{"task": ""}},
		{"whitespace only", map[string]any{"task": "   "}},
		{"tabs and newlines", map[string]any{"task": "\t\n  "}},
		{"missing task key", map[string]any{"label": "test"}},
		{"wrong type", map[string]any{"task": 123}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tool.Execute(ctx, tt.args)
			if result == nil {
				t.Fatal("Result should not be nil")
			}
			if !result.IsError {
				t.Error("Expected error for invalid task parameter")
			}
			if !strings.Contains(result.ForLLM, "task is required") {
				t.Errorf("Error message should mention 'task is required', got: %s", result.ForLLM)
			}
		})
	}
}

func TestSpawnTool_Execute_ValidTask(t *testing.T) {
	manager := NewSubagentManager("test-model", t.TempDir())
	tool := NewSpawnTool(manager)
	spawner := &mockSpawner{done: make(chan struct{})}
	manager.SetSpawner(spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = toolshared.WithToolSessionContext(ctx, "main", "requester-session", nil)
	ctx = toolshared.WithToolHistoryDisabled(ctx, true)
	args := map[string]any{
		"task":     "Write a haiku about coding",
		"label":    "haiku-task",
		"agent_id": "research",
	}

	result := tool.Execute(ctx, args)
	if result == nil {
		t.Fatal("Result should not be nil")
	}
	if result.IsError {
		t.Errorf("Expected success for valid task, got error: %s", result.ForLLM)
	}
	if !result.Control.Async {
		t.Error("SpawnTool should return async result")
	}
	<-spawner.done
	if spawner.lastConfig.TargetAgentID != "research" {
		t.Errorf("TargetAgentID = %q, want research", spawner.lastConfig.TargetAgentID)
	}
	if !spawner.lastConfig.Critical {
		t.Error("SpawnTool should mark background subturns as critical")
	}
	tasks := manager.taskRegistry.List()
	if len(tasks) != 1 {
		t.Fatalf("task registry count = %d, want 1", len(tasks))
	}
	taskID := tasks[0].TaskID
	if !strings.HasPrefix(taskID, "subagent-") {
		t.Fatalf("task ID = %q, want subagent-*", taskID)
	}
	if !strings.Contains(result.ForLLM, taskID) ||
		!strings.Contains(result.ForLLM, "acceptance only") ||
		!strings.Contains(result.ForLLM, "task_status") {
		t.Fatalf("spawn acknowledgement lacks durable status guidance: %q", result.ForLLM)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		rec, ok := manager.taskRegistry.Get(taskID)
		if ok && rec.Status == taskregistry.StatusSucceeded {
			if rec.OwnerKey != "main" || rec.RequesterSessionKey != "requester-session" ||
				!rec.HistoryPolicyKnown ||
				!rec.HistoryDisabled {
				t.Fatalf("task ownership = %+v", rec)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task registry never observed succeeded result for %q", taskID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSpawnTool_BrowserObjectivePreflightRejectsBeforeAsyncStart(t *testing.T) {
	manager := NewSubagentManager("test-model", t.TempDir())
	tool := NewSpawnTool(manager)
	spawner := &mockSpawner{done: make(chan struct{})}
	manager.SetSpawner(spawner)
	tool.SetObjectiveChecklistRequirement(func(targetAgentID string) bool {
		return targetAgentID == "browser"
	})

	result := tool.ExecuteAsync(context.Background(), map[string]any{
		"agent_id": "browser",
		"task":     "inspect two listings",
	}, func(context.Context, *toolshared.ToolResult) {
		t.Error("callback must not run when browser objective preflight fails")
	})

	if result == nil || !result.IsError || result.Control.Async {
		t.Fatalf("result = %#v, want synchronous error", result)
	}
	if !strings.Contains(result.ForLLM, "retry spawn") {
		t.Fatalf("ForLLM = %q, want retry guidance", result.ForLLM)
	}
	if tasks := manager.taskRegistry.List(); len(tasks) != 0 {
		t.Fatalf("spawned tasks = %#v, want none", tasks)
	}
	select {
	case <-spawner.done:
		t.Fatal("browser child started without objective_items")
	default:
	}
}

func TestSpawnTool_Execute_NilManager(t *testing.T) {
	tool := NewSpawnTool(nil)

	ctx := context.Background()
	args := map[string]any{"task": "test task"}

	result := tool.Execute(ctx, args)
	if !result.IsError {
		t.Error("Expected error for nil manager")
	}
	if !strings.Contains(result.ForLLM, "Subagent manager not configured") {
		t.Errorf("Error message should mention manager not configured, got: %s", result.ForLLM)
	}
}

func TestSpawnTool_Execute_RequiresChildRunner(t *testing.T) {
	manager := NewSubagentManager("test-model", t.TempDir())
	result := NewSpawnTool(manager).Execute(context.Background(), map[string]any{"task": "test task"})
	if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "child runner is unavailable") {
		t.Fatalf("spawn without child runner = %#v", result)
	}
	if records := manager.taskRegistry.List(); len(records) != 0 {
		t.Fatalf("unavailable child runner admitted tasks: %#v", records)
	}
}

func TestSpawnTool_TaskStatusSeesSpawnedTask(t *testing.T) {
	manager := NewSubagentManager("test-model", t.TempDir())
	spawnTool := NewSpawnTool(manager)
	spawner := &mockSpawner{done: make(chan struct{})}
	manager.SetSpawner(spawner)
	statusTool := NewTaskStatusTool(manager.taskRegistry, nil)

	ctx := toolshared.WithToolContext(context.Background(), "telegram", "chat-1")
	args := map[string]any{
		"task":     "Write a haiku about coding",
		"label":    "haiku-task",
		"agent_id": "deep-research",
	}

	result := spawnTool.Execute(ctx, args)
	if result == nil {
		t.Fatal("Result should not be nil")
	}
	if result.IsError {
		t.Fatalf("Expected success for valid task, got error: %s", result.ForLLM)
	}
	if !result.Control.Async {
		t.Fatal("SpawnTool should return async result")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		status := statusTool.Execute(ctx, map[string]any{})
		if status == nil {
			t.Fatal("status result should not be nil")
		}
		if status.IsError {
			t.Fatalf("task_status returned error: %s", status.ForLLM)
		}
		if strings.Contains(status.ForLLM, "subagent-") {
			if !strings.Contains(status.ForLLM, "Write a haiku about coding") {
				t.Fatalf("expected task in status output, got: %s", status.ForLLM)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task_status never observed spawned task; last output: %s", status.ForLLM)
		}
		time.Sleep(10 * time.Millisecond)
	}

	<-spawner.done
	deadline = time.Now().Add(2 * time.Second)
	for {
		tasks := manager.taskRegistry.List()
		if len(tasks) == 1 && tasks[0].Status == taskregistry.StatusSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("spawned tasks did not complete: %#v", tasks)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSpawnTool_ExecuteAsync_MarksCallbackResultUserOnly(t *testing.T) {
	manager := NewSubagentManager("test-model", t.TempDir())
	tool := NewSpawnTool(manager)
	spawner := &mockSpawner{}
	manager.SetSpawner(spawner)

	done := make(chan *toolshared.ToolResult, 1)
	result := tool.ExecuteAsync(context.Background(), map[string]any{
		"task": "Write a haiku about coding",
	}, func(_ context.Context, res *toolshared.ToolResult) {
		done <- res
	})

	if result == nil || !result.Control.Async {
		t.Fatal("expected async acknowledgment result")
	}

	select {
	case cbResult := <-done:
		if cbResult == nil {
			t.Fatal("expected callback result")
		}
		if cbResult.Delivery.AsyncMode != toolshared.AsyncDeliveryUserOnly {
			t.Fatalf("AsyncDelivery = %q, want %q", cbResult.Delivery.AsyncMode, toolshared.AsyncDeliveryUserOnly)
		}
		if !strings.HasPrefix(cbResult.Control.TaskID, "subagent-") {
			t.Fatalf("AsyncTaskID = %q, want subagent-*", cbResult.Control.TaskID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for spawn callback result")
	}
}

func TestSpawnTool_PropagatesDurableTaskIDToSubTurn(t *testing.T) {
	manager := NewSubagentManager("test-model", t.TempDir())
	tool := NewSpawnTool(manager)
	spawner := &mockSpawner{}
	manager.SetSpawner(spawner)
	completed := make(chan struct{})

	result := tool.ExecuteAsync(context.Background(), map[string]any{
		"task":          "wait for deployment mode",
		"delivery_mode": string(toolshared.AsyncDeliveryParentOnly),
	}, func(context.Context, *toolshared.ToolResult) {
		close(completed)
	})
	if result == nil || !result.Control.Async {
		t.Fatalf("spawn result = %#v", result)
	}
	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for spawned subturn completion")
	}
	if !strings.HasPrefix(spawner.lastConfig.TaskID, "subagent-") {
		t.Fatalf("subturn TaskID = %q", spawner.lastConfig.TaskID)
	}
	rec, ok := manager.taskRegistry.Get(spawner.lastConfig.TaskID)
	if !ok || rec.DeliveryMode != string(toolshared.AsyncDeliveryParentOnly) {
		t.Fatalf("durable spawn task = %#v", rec)
	}
}

func TestSpawnTool_ExecuteAsync_RespectsExplicitDeliveryMode(t *testing.T) {
	manager := NewSubagentManager("test-model", t.TempDir())
	tool := NewSpawnTool(manager)
	spawner := &mockSpawner{}
	manager.SetSpawner(spawner)

	done := make(chan *toolshared.ToolResult, 1)
	result := tool.ExecuteAsync(context.Background(), map[string]any{
		"task":          "Write a haiku about coding",
		"delivery_mode": string(toolshared.AsyncDeliveryUserAndParent),
	}, func(_ context.Context, res *toolshared.ToolResult) {
		done <- res
	})

	if result == nil || !result.Control.Async {
		t.Fatal("expected async acknowledgment result")
	}

	select {
	case cbResult := <-done:
		if cbResult == nil {
			t.Fatal("expected callback result")
		}
		if cbResult.Delivery.AsyncMode != toolshared.AsyncDeliveryUserAndParent {
			t.Fatalf("AsyncDelivery = %q, want %q", cbResult.Delivery.AsyncMode, toolshared.AsyncDeliveryUserAndParent)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for spawn callback result")
	}
}

func TestSpawnTool_Execute_InvalidDeliveryMode(t *testing.T) {
	manager := NewSubagentManager("test-model", t.TempDir())
	tool := NewSpawnTool(manager)

	tests := []map[string]any{
		{"task": "test", "delivery_mode": 123},
		{"task": "test", "delivery_mode": "wrong"},
	}

	for _, args := range tests {
		result := tool.Execute(context.Background(), args)
		if result == nil {
			t.Fatal("expected result")
		}
		if !result.IsError {
			t.Fatalf("expected error for args=%v", args)
		}
		if !strings.Contains(result.ForLLM, "delivery_mode") {
			t.Fatalf("expected delivery_mode error, got: %s", result.ForLLM)
		}
	}
}
