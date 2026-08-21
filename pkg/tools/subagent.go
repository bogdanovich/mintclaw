package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// SubTurnSpawner is an interface for spawning sub-turns.
// This avoids circular dependency between tools and agent packages.
type SubTurnSpawner interface {
	SpawnSubTurn(ctx context.Context, cfg SubTurnConfig) (*toolshared.ToolResult, error)
}

// SubTurnConfig holds configuration for spawning a sub-turn.
type SubTurnConfig struct {
	Model              string
	Tools              []toolshared.Tool
	SystemPrompt       string
	MaxTokens          int
	Temperature        float64
	Async              bool          // true for async (spawn), false for sync (subagent)
	Critical           bool          // continue running after parent finishes gracefully
	Timeout            time.Duration // 0 = use default (5 minutes)
	MaxContextRunes    int           // 0 = auto, -1 = no limit, >0 = explicit limit
	ActualSystemPrompt string
	InitialMessages    []providers.Message
	InitialTokenBudget *atomic.Int64 // Shared token budget for team members; nil if no budget
	TargetAgentID      string        // If set, run as this agent (its workspace, model, tools)
	DeliveryMode       toolshared.AsyncDeliveryMode
	TaskID             string // Durable task owning this child turn, when one exists.
	ObjectiveItems     []toolshared.ObjectiveSpec
}

type SubagentManager struct {
	mu             sync.RWMutex
	defaultModel   string
	maxTokens      int
	temperature    float64
	hasMaxTokens   bool
	hasTemperature bool
	spawner        SubTurnSpawner
	taskRegistry   *taskregistry.Registry
}

// NewSubagentManagerWithRegistry requires the canonical task registry shared
// by every manager that owns the same workspace.
func NewSubagentManagerWithRegistry(
	defaultModel string,
	registry *taskregistry.Registry,
) *SubagentManager {
	return &SubagentManager{
		defaultModel: defaultModel,
		taskRegistry: registry,
	}
}

func (sm *SubagentManager) SetSpawner(spawner SubTurnSpawner) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.spawner = spawner
}

// SetLLMOptions sets max tokens and temperature for subagent LLM calls.
func (sm *SubagentManager) SetLLMOptions(maxTokens int, temperature float64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.maxTokens = maxTokens
	sm.hasMaxTokens = true
	sm.temperature = temperature
	sm.hasTemperature = true
}

func (sm *SubagentManager) Spawn(
	ctx context.Context,
	task, label, agentID, originChannel, originChatID string,
	deliveryMode toolshared.AsyncDeliveryMode,
	callback toolshared.AsyncCallback,
	objectiveSets ...[]toolshared.ObjectiveSpec,
) (string, error) {
	if sm == nil || sm.taskRegistry == nil {
		return "", errors.New("subagent task registry is unavailable")
	}
	sm.mu.RLock()
	runnerAvailable := sm.spawner != nil
	sm.mu.RUnlock()
	if !runnerAvailable {
		return "", errors.New("subagent child runner is unavailable")
	}

	taskID := "subagent-" + uuid.NewString()
	var objectiveItems []toolshared.ObjectiveSpec
	if len(objectiveSets) > 0 {
		objectiveItems = objectiveSets[0]
	}
	now := time.Now().UnixMilli()
	record := taskregistry.Record{
		TaskID:              taskID,
		Runtime:             taskregistry.RuntimeSubagent,
		TaskKind:            "spawn",
		Channel:             originChannel,
		ChatID:              originChatID,
		AgentID:             agentID,
		OwnerKey:            toolshared.ToolAgentID(ctx),
		RequesterSessionKey: toolshared.ToolSessionKey(ctx),
		HistoryPolicyKnown:  true,
		HistoryDisabled:     toolshared.ToolHistoryDisabled(ctx),
		Label:               label,
		Task:                task,
		Status:              taskregistry.StatusRunning,
		DeliveryStatus:      taskregistry.DeliveryPending,
		NotifyPolicy:        taskregistry.NotifyDoneOnly,
		DeliveryMode:        string(deliveryMode),
		CreatedAt:           now,
		StartedAt:           now,
		LastEventAt:         now,
	}
	if err := sm.taskRegistry.Create(record); err != nil {
		return "", fmt.Errorf("persist spawned subagent: %w", err)
	}

	// Start task in background with context cancellation support
	go sm.runTask(ctx, record, objectiveItems, callback)

	if label != "" {
		return fmt.Sprintf(
			"Spawned subagent '%s' for task: %s (task_id: %s). This confirms acceptance only; use task_status to check whether it is still running.",
			label,
			task,
			taskID,
		), nil
	}
	return fmt.Sprintf(
		"Spawned subagent for task: %s (task_id: %s). This confirms acceptance only; use task_status to check whether it is still running.",
		task,
		taskID,
	), nil
}

func objectiveItemsParameter() map[string]any {
	return map[string]any{
		"type":        "array",
		"description": "Declared verification contract for the child. Required and validated before browser-capable children execute. Include every outcome the caller needs verified; use external_action for state changes and result for read-only findings. The runtime does not infer omitted intent from task prose.",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"item": map[string]any{"type": "string"},
				"kind": map[string]any{"type": "string", "enum": []string{"result", "external_action"}},
			},
			"required": []string{"item", "kind"},
		},
	}
}

func parseObjectiveItems(raw any) ([]toolshared.ObjectiveSpec, error) {
	if raw == nil {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("objective_items must be an array")
	}
	if len(values) > 64 {
		return nil, fmt.Errorf("objective_items cannot contain more than 64 entries")
	}
	items := make([]toolshared.ObjectiveSpec, 0, len(values))
	for index, value := range values {
		entry, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("objective_items[%d] must be an object", index)
		}
		item, _ := entry["item"].(string)
		kind, _ := entry["kind"].(string)
		item, kind = strings.TrimSpace(item), strings.TrimSpace(kind)
		if item == "" || (kind != "result" && kind != "external_action") {
			return nil, fmt.Errorf("objective_items[%d] requires item and kind result|external_action", index)
		}
		items = append(items, toolshared.ObjectiveSpec{Item: item, Kind: kind})
	}
	return items, nil
}

func (sm *SubagentManager) runTask(
	ctx context.Context,
	task taskregistry.Record,
	objectiveItems []toolshared.ObjectiveSpec,
	callback toolshared.AsyncCallback,
) {
	// Check if context is already canceled before starting
	select {
	case <-ctx.Done():
		sm.recordTaskOrLog(
			task.TaskID,
			taskregistry.StatusCancelled,
			taskregistry.DeliveryNotApplicable,
			"Task canceled before execution",
		)
		return
	default:
	}

	stopHeartbeat := startTaskRegistryHeartbeat(
		ctx,
		sm.taskRegistry,
		task.TaskID,
		"spawned subagent is still running",
	)
	defer stopHeartbeat()

	result, err := sm.spawnSubTurn(ctx, SubTurnConfig{
		TaskID:         task.TaskID,
		TargetAgentID:  task.AgentID,
		SystemPrompt:   buildSpawnSystemPrompt(task.Task, task.Label),
		Critical:       true,
		ObjectiveItems: append([]toolshared.ObjectiveSpec(nil), objectiveItems...),
	})
	if result == nil && err == nil {
		err = errors.New("subagent child runner returned no result")
	}
	if result != nil && result.Control.TaskSuspended {
		return
	}

	if err != nil {
		status := taskregistry.StatusFailed
		summary := fmt.Sprintf("Error: %v", err)
		// Only report cancellation when cancellation is the actual cause.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = taskregistry.StatusCancelled
			summary = "Task canceled during execution"
		}
		sm.recordTaskOrLog(task.TaskID, status, taskregistry.DeliveryPending, summary)
		result = &toolshared.ToolResult{
			ForLLM:  summary,
			ForUser: summary,
			IsError: true,
			Err:     err,
		}
	} else {
		sm.recordTaskResult(task.TaskID, result)
	}
	if result != nil {
		result.WithTaskID(task.TaskID)
	}
	if callback != nil && result != nil {
		callback(ctx, result)
	}
}

func (sm *SubagentManager) spawnSubTurn(
	ctx context.Context,
	cfg SubTurnConfig,
) (*toolshared.ToolResult, error) {
	if sm == nil {
		return nil, errors.New("subagent child runner is unavailable")
	}
	sm.mu.RLock()
	spawner := sm.spawner
	defaultModel := sm.defaultModel
	maxTokens := sm.maxTokens
	temperature := sm.temperature
	hasMaxTokens := sm.hasMaxTokens
	hasTemperature := sm.hasTemperature
	sm.mu.RUnlock()
	if spawner == nil {
		return nil, errors.New("subagent child runner is unavailable")
	}
	if cfg.Model == "" {
		cfg.Model = defaultModel
	}
	if cfg.MaxTokens == 0 && hasMaxTokens {
		cfg.MaxTokens = maxTokens
	}
	if cfg.Temperature == 0 && hasTemperature {
		cfg.Temperature = temperature
	}
	return spawner.SpawnSubTurn(ctx, cfg)
}

func (sm *SubagentManager) updateTask(
	taskID string,
	status taskregistry.Status,
	delivery taskregistry.DeliveryStatus,
	summary string,
	mutate func(*taskregistry.Record),
) error {
	if sm == nil || sm.taskRegistry == nil {
		return errors.New("subagent task registry is unavailable")
	}
	return sm.taskRegistry.Update(taskID, func(stored *taskregistry.Record) {
		now := time.Now().UnixMilli()
		stored.Status = status
		stored.DeliveryStatus = delivery
		stored.LastEventAt = now
		if status == taskregistry.StatusSucceeded || status == taskregistry.StatusFailed ||
			status == taskregistry.StatusCancelled || status == taskregistry.StatusTimedOut {
			stored.EndedAt = now
			stored.TerminalSummary = summary
		}
		if status == taskregistry.StatusFailed {
			stored.Error = summary
		} else {
			stored.Error = ""
		}
		if mutate != nil {
			mutate(stored)
		}
	})
}

func (sm *SubagentManager) recordTaskOrLog(
	taskID string,
	status taskregistry.Status,
	delivery taskregistry.DeliveryStatus,
	summary string,
) {
	if err := sm.updateTask(taskID, status, delivery, summary, nil); err != nil {
		logger.WarnCF("subagent", "Failed to persist subagent task state", map[string]any{
			"task_id": taskID,
			"status":  status,
			"error":   err.Error(),
		})
	}
}

func (sm *SubagentManager) recordTaskResult(taskID string, result *toolshared.ToolResult) {
	if sm == nil || sm.taskRegistry == nil {
		return
	}
	summary := ""
	if result != nil {
		summary = result.ContentForLLM()
	}
	delivery := taskregistry.DeliveryPending
	if result == nil || (result.Delivery.Intent == toolshared.DeliverySilent &&
		result.Delivery.AsyncMode == toolshared.AsyncDeliveryParentOnly) {
		delivery = taskregistry.DeliveryNotApplicable
	}
	deliverable := taskDeliverable(result)
	if err := sm.updateTask(
		taskID,
		terminalTaskStatusForResult(result),
		delivery,
		summary,
		func(rec *taskregistry.Record) {
			rec.Deliverable = deliverable
		},
	); err != nil {
		logger.WarnCF("subagent", "Failed to persist subagent task result", map[string]any{
			"task_id": taskID,
			"error":   err.Error(),
		})
	}
}

func terminalTaskStatusForResult(result *toolshared.ToolResult) taskregistry.Status {
	if result == nil || result.Deliverable == nil {
		return taskregistry.TerminalStatusForObjectiveOutcome(nil)
	}
	return taskregistry.TerminalStatusForObjectiveOutcome(result.Deliverable.ObjectiveOutcome)
}

func taskDeliverable(result *toolshared.ToolResult) *taskresult.Deliverable {
	if result == nil || result.Deliverable == nil {
		return nil
	}
	deliverable := taskresult.CloneDeliverable(result.Deliverable)
	if deliverable.Text == "" && len(deliverable.Artifacts) == 0 && len(deliverable.Metadata) == 0 &&
		deliverable.Report == nil && deliverable.ObjectiveOutcome == nil {
		return nil
	}
	return deliverable
}

// SubagentTool executes a subagent task synchronously and returns the result.
// It directly calls SubTurnSpawner with Async=false for synchronous execution.
type SubagentTool struct {
	manager *SubagentManager
}

func NewSubagentTool(manager *SubagentManager) *SubagentTool {
	return &SubagentTool{manager: manager}
}

func (t *SubagentTool) Name() string {
	return "subagent"
}

func (t *SubagentTool) Description() string {
	return "Execute a subagent task synchronously and return the result. Use this for delegating specific tasks to an independent agent instance. Returns execution summary to user and full details to LLM."
}

func (t *SubagentTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "The task for subagent to complete",
			},
			"label": map[string]any{
				"type":        "string",
				"description": "Optional short label for the task (for display)",
			},
			"objective_items": objectiveItemsParameter(),
		},
		"required": []string{"task"},
	}
}

func (t *SubagentTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	task, ok := args["task"].(string)
	if !ok {
		return toolshared.ErrorResult("task is required").WithError(fmt.Errorf("task parameter is required"))
	}

	label, ok := args["label"].(string)
	if !ok {
		label = ""
	}
	objectiveItems, parseErr := parseObjectiveItems(args["objective_items"])
	if parseErr != nil {
		return toolshared.ErrorResult(parseErr.Error()).WithError(parseErr)
	}

	// Build system prompt for subagent
	systemPrompt := fmt.Sprintf(
		`You are a subagent. Complete the given task independently and provide a clear, concise result.

Task: %s`,
		task,
	)

	if label != "" {
		systemPrompt = fmt.Sprintf(
			`You are a subagent labeled "%s". Complete the given task independently and provide a clear, concise result.

Task: %s`,
			label,
			task,
		)
	}

	if t.manager != nil {
		result, err := t.manager.spawnSubTurn(ctx, SubTurnConfig{
			Tools:          nil, // Will inherit from parent via context
			SystemPrompt:   systemPrompt,
			Async:          false, // Synchronous execution
			ObjectiveItems: objectiveItems,
		})
		if err != nil {
			return toolshared.ErrorResult(fmt.Sprintf("Subagent execution failed: %v", err)).WithError(err)
		}
		if result == nil {
			return toolshared.ErrorResult("Subagent execution returned no result")
		}
		if result.Control.TaskSuspended {
			return result
		}

		// Format result for display
		userContent := result.ForLLM
		if result.ForUser != "" {
			userContent = result.ForUser
		}
		maxUserLen := 500
		if len(userContent) > maxUserLen {
			userContent = userContent[:maxUserLen] + "..."
		}

		labelStr := label
		if labelStr == "" {
			labelStr = "(unnamed)"
		}
		llmContent := fmt.Sprintf("Subagent task completed:\nLabel: %s\nResult: %s",
			labelStr, result.ForLLM)

		result.ForLLM = llmContent
		result.ForUser = userContent
		result.Control.Async = false
		result.Delivery.Intent = toolshared.DeliveryDefault
		return result
	}

	// Fallback: spawner not configured
	return toolshared.ErrorResult("Subagent manager not configured").WithError(fmt.Errorf("spawner not set"))
}
