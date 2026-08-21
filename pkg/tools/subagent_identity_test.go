package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func NewSubagentManager(
	defaultModel, workspace string,
) *SubagentManager {
	return NewSubagentManagerWithRegistry(
		defaultModel,
		taskregistry.NewRegistry(taskregistry.WorkspaceStorePath(workspace)),
	)
}

func TestSubagentManagersSharingRegistryAllocateDistinctTaskIDs(t *testing.T) {
	workspace := t.TempDir()
	registry := taskregistry.NewRegistry(
		taskregistry.WorkspaceStorePath(workspace),
	)
	first := NewSubagentManagerWithRegistry("model", registry)
	second := NewSubagentManagerWithRegistry("model", registry)
	first.SetSpawner(&mockSpawner{})
	second.SetSpawner(&mockSpawner{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := first.Spawn(
		ctx, "first", "", "main", "telegram", "chat",
		toolshared.AsyncDeliveryUserOnly, nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Spawn(
		ctx, "second", "", "main", "telegram", "chat",
		toolshared.AsyncDeliveryUserOnly, nil,
	); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		records := registry.List()
		if len(records) == 2 &&
			records[0].Status == taskregistry.StatusCancelled &&
			records[1].Status == taskregistry.StatusCancelled {
			if records[0].TaskID == records[1].TaskID {
				t.Fatalf("managers reused task ID %q", records[0].TaskID)
			}
			for _, record := range records {
				if !strings.HasPrefix(record.TaskID, "subagent-") ||
					record.GenerationID == "" {
					t.Fatalf("invalid durable task identity: %#v", record)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("shared registry tasks = %#v, want 2", records)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSubagentSpawnFailsBeforeLaunchWhenTaskCreateFails(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := taskregistry.NewRegistry(filepath.Join(blocked, "tasks.json"))
	manager := NewSubagentManagerWithRegistry("model", registry)
	manager.SetSpawner(&mockSpawner{})

	_, err := manager.Spawn(
		context.Background(),
		"must not launch",
		"",
		"main",
		"telegram",
		"chat",
		toolshared.AsyncDeliveryUserOnly,
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "persist spawned subagent") {
		t.Fatalf("Spawn() error = %v", err)
	}
	if tasks := registry.List(); len(tasks) != 0 {
		t.Fatalf("failed durable create published task records: %#v", tasks)
	}
}

func TestSubagentStatusUpdatePreservesDurableGeneration(t *testing.T) {
	registry := taskregistry.NewRegistry(filepath.Join(t.TempDir(), "tasks.json"))
	manager := NewSubagentManagerWithRegistry("model", registry)
	now := time.Now().UnixMilli()
	task := taskregistry.Record{
		TaskID: "subagent-" + strings.Repeat("a", 36), Runtime: taskregistry.RuntimeSubagent,
		TaskKind: "spawn", Task: "test", Status: taskregistry.StatusRunning,
		DeliveryStatus: taskregistry.DeliveryPending, CreatedAt: now, StartedAt: now,
	}
	if err := registry.Create(task); err != nil {
		t.Fatal(err)
	}
	created, _ := registry.Get(task.TaskID)
	if err := registry.Update(task.TaskID, func(record *taskregistry.Record) {
		record.Deliverable = &taskresult.Deliverable{Text: "existing"}
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.updateTask(
		task.TaskID,
		taskregistry.StatusSucceeded,
		taskregistry.DeliveryDelivered,
		"done",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	completed, _ := registry.Get(task.TaskID)
	if completed.GenerationID != created.GenerationID ||
		completed.CreatedAt != created.CreatedAt ||
		completed.StartedAt != created.StartedAt ||
		completed.Status != taskregistry.StatusSucceeded ||
		completed.TerminalSummary != "done" ||
		completed.Deliverable == nil ||
		completed.Deliverable.Text != "existing" {
		t.Fatalf("updated durable task = %#v, created = %#v", completed, created)
	}
}

func TestSubagentResultPersistsTerminalStateAndPayloadTogether(t *testing.T) {
	registry := taskregistry.NewRegistry(filepath.Join(t.TempDir(), "tasks.json"))
	manager := NewSubagentManagerWithRegistry("model", registry)
	now := time.Now().UnixMilli()
	task := taskregistry.Record{
		TaskID: "subagent-" + strings.Repeat("b", 36), Runtime: taskregistry.RuntimeSubagent,
		TaskKind: "spawn", Task: "test", Status: taskregistry.StatusRunning,
		DeliveryStatus: taskregistry.DeliveryPending, CreatedAt: now, StartedAt: now,
	}
	if err := registry.Create(task); err != nil {
		t.Fatal(err)
	}

	manager.recordTaskResult(task.TaskID, &toolshared.ToolResult{
		ForLLM: "done",
		Deliverable: &taskresult.Deliverable{
			Text: "structured result",
		},
	})

	completed, ok := registry.Get(task.TaskID)
	if !ok {
		t.Fatal("completed task not found")
	}
	if completed.Status != taskregistry.StatusSucceeded ||
		!strings.Contains(completed.TerminalSummary, "done") ||
		completed.Deliverable == nil ||
		completed.Deliverable.Text != "structured result" {
		t.Fatalf("completed durable task = %#v", completed)
	}
	events := registry.ListEvents(task.TaskID)
	statusChanges := 0
	for _, event := range events {
		if event.Type == taskregistry.EventTaskStatusChanged {
			statusChanges++
		}
	}
	if statusChanges != 1 {
		t.Fatalf("terminal events = %#v", events)
	}
}
