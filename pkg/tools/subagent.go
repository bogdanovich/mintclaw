package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	Model string
	// ModelOverride is an exact configured model_name scoped to this child turn.
	ModelOverride      string
	Tools              []toolshared.Tool
	TaskPrompt         string
	MaxTokens          int
	Temperature        float64
	Async              bool          // true for async (spawn), false for sync (subagent)
	Critical           bool          // continue running after parent finishes gracefully
	Timeout            time.Duration // 0 = use default (5 minutes)
	MaxContextRunes    int           // 0 = auto, -1 = no limit, >0 = explicit limit
	InitialMessages    []providers.Message
	InitialTokenBudget *atomic.Int64 // Shared token budget for team members; nil if no budget
	TargetAgentID      string        // If set, run as this agent (its workspace, model, tools)
	DeliveryMode       toolshared.AsyncDeliveryMode
	TaskID             string // Durable task owning this child turn, when one exists.
	ObjectiveItems     []toolshared.ObjectiveSpec
}

type SubagentManager struct {
	defaultModel string
	models       []string
	maxTokens    int
	temperature  float64
	spawner      SubTurnSpawner
	taskRegistry *taskregistry.Registry
}

// SubagentManagerConfig contains the immutable dependencies and LLM defaults
// shared by synchronous and background child turns.
type SubagentManagerConfig struct {
	DefaultModel    string
	AvailableModels []string
	MaxTokens       int
	Temperature     float64
	Spawner         SubTurnSpawner
	TaskRegistry    *taskregistry.Registry
}

// NewSubagentManager requires the canonical task registry shared by every
// manager that owns the same workspace and the child-turn package boundary.
func NewSubagentManager(config SubagentManagerConfig) (*SubagentManager, error) {
	if config.Spawner == nil {
		return nil, errors.New("subagent child runner is required")
	}
	if config.TaskRegistry == nil {
		return nil, errors.New("subagent task registry is required")
	}
	return &SubagentManager{
		defaultModel: config.DefaultModel,
		models:       normalizeAvailableModels(config.AvailableModels),
		maxTokens:    config.MaxTokens,
		temperature:  config.Temperature,
		spawner:      config.Spawner,
		taskRegistry: config.TaskRegistry,
	}, nil
}

func (sm *SubagentManager) Spawn(
	ctx context.Context,
	task, label, agentID, originChannel, originChatID string,
	deliveryMode toolshared.AsyncDeliveryMode,
	callback toolshared.AsyncCallback,
	objectiveSets ...[]toolshared.ObjectiveSpec,
) (string, error) {
	return sm.spawnWithModel(
		ctx,
		task,
		label,
		agentID,
		"",
		originChannel,
		originChatID,
		deliveryMode,
		callback,
		objectiveSets...,
	)
}

func (sm *SubagentManager) spawnWithModel(
	ctx context.Context,
	task, label, agentID, modelOverride, originChannel, originChatID string,
	deliveryMode toolshared.AsyncDeliveryMode,
	callback toolshared.AsyncCallback,
	objectiveSets ...[]toolshared.ObjectiveSpec,
) (string, error) {
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
	go sm.runTask(ctx, record, modelOverride, objectiveItems, callback)

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

func normalizedObjectiveKinds(allowedKinds []string) []string {
	if len(allowedKinds) == 0 {
		return []string{
			taskresult.ObjectiveKindResult,
			taskresult.ObjectiveKindExternalAction,
			taskresult.ObjectiveKindLiveHandoff,
		}
	}
	kinds := make([]string, 0, len(allowedKinds))
	for _, candidate := range allowedKinds {
		candidate = strings.TrimSpace(candidate)
		if objectiveKindAllowed(kinds, candidate) {
			continue
		}
		switch candidate {
		case taskresult.ObjectiveKindResult,
			taskresult.ObjectiveKindExternalAction,
			taskresult.ObjectiveKindLiveHandoff:
			kinds = append(kinds, candidate)
		}
	}
	return kinds
}

func objectiveKindAllowed(allowedKinds []string, kind string) bool {
	for _, allowed := range allowedKinds {
		if kind == allowed {
			return true
		}
	}
	return false
}

func objectiveItemsParameter(allowedKinds ...string) map[string]any {
	kinds := normalizedObjectiveKinds(allowedKinds)
	description := "Declared verification contract for the child. Required for targets configured to use it. " +
		"Include every outcome the caller needs verified. Use external_action only for a durable requested external " +
		"state change such as publish, send, purchase, delete, save, or submit. Opening, navigating, observing, " +
		"reading, and closing a browser session are result objectives, never external_action objectives."
	kindDescription := "Use result for read-only findings and ordinary browser lifecycle work, and external_action " +
		"only for a durable requested external state change."
	if objectiveKindAllowed(kinds, taskresult.ObjectiveKindLiveHandoff) {
		description += " Use live_handoff only when a live resource must remain available under human control; it " +
			"completes only through a tool-issued durable suspension receipt, so opening a resource or claiming it " +
			"was left open is not enough."
		kindDescription += " Use live_handoff only to preserve a live resource under human control."
	} else {
		description += " Live handoff is unavailable in synchronous subagent execution; use durable spawn or " +
			"delegate instead."
	}
	description += " If an external action should occur only after approval, include the pending state change as " +
		"external_action and instruct the child to invoke the approval-bound tool so the runtime can suspend before " +
		"commit; never model the approval boundary as a result or ask the child to stop before the tool call. The " +
		"runtime does not infer omitted intent from task prose."
	return map[string]any{
		"type":        "array",
		"description": description,
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"item": map[string]any{"type": "string"},
				"kind": map[string]any{
					"type":        "string",
					"enum":        kinds,
					"description": kindDescription,
				},
				"acceptance": map[string]any{
					"type":                 "object",
					"description":          "Optional machine-checkable shape for a result objective. Use records only for non-exact collections or tables whose every field value is a non-empty string. Use text for prose, every exact JSON value (including objects and arrays), or results containing booleans, numbers, or null values. Use artifact for stable output references.",
					"additionalProperties": false,
					"properties": map[string]any{
						"output_kind": map[string]any{
							"type":        "string",
							"enum":        []string{"text", "records", "artifact"},
							"description": "records is string-only non-exact tabular data; choose text for every exact JSON value, including objects and arrays, or typed scalar fields.",
						},
						"required_fields": map[string]any{
							"type": "array", "items": map[string]any{"type": "string"},
						},
						"min_items": map[string]any{"type": "integer"},
					},
					"required": []string{"output_kind"},
				},
			},
			"required": []string{"item", "kind"},
		},
	}
}

func parseObjectiveItems(raw any, allowedKinds ...string) ([]toolshared.ObjectiveSpec, error) {
	if raw == nil {
		return nil, nil
	}
	kinds := normalizedObjectiveKinds(allowedKinds)
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
		if item == "" || !objectiveKindAllowed(kinds, kind) {
			return nil, fmt.Errorf(
				"objective_items[%d] requires item and kind %s",
				index,
				strings.Join(kinds, "|"),
			)
		}
		acceptance, err := parseObjectiveAcceptance(entry["acceptance"], kind)
		if err != nil {
			return nil, fmt.Errorf("objective_items[%d] acceptance: %w", index, err)
		}
		items = append(items, toolshared.ObjectiveSpec{Item: item, Kind: kind, Acceptance: acceptance})
	}
	return items, nil
}

func parseObjectiveAcceptance(raw any, objectiveKind string) (*taskresult.ObjectiveAcceptance, error) {
	if raw == nil {
		return nil, nil
	}
	if objectiveKind != taskresult.ObjectiveKindResult {
		return nil, errors.New("is only valid for result objectives")
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("must be an object")
	}
	for key := range value {
		switch key {
		case "output_kind", "required_fields", "min_items":
		default:
			return nil, fmt.Errorf("contains unknown field %q", key)
		}
	}
	outputKind, _ := value["output_kind"].(string)
	outputKind = strings.TrimSpace(outputKind)
	if outputKind != "text" && outputKind != "records" && outputKind != "artifact" {
		return nil, errors.New("requires output_kind text|records|artifact")
	}
	acceptance := &taskresult.ObjectiveAcceptance{OutputKind: outputKind}
	if rawMin, found := value["min_items"]; found {
		minItems, ok := numericInt(rawMin)
		if !ok || minItems < 0 || minItems > 1024 {
			return nil, errors.New("min_items must be an integer between 0 and 1024")
		}
		acceptance.MinItems = minItems
	}
	if rawFields, found := value["required_fields"]; found {
		fields, ok := rawFields.([]any)
		if !ok || len(fields) > 32 {
			return nil, errors.New("required_fields must be an array of at most 32 strings")
		}
		seen := make(map[string]struct{}, len(fields))
		for _, rawField := range fields {
			field, ok := rawField.(string)
			field = strings.TrimSpace(field)
			if !ok || field == "" || len([]rune(field)) > 64 {
				return nil, errors.New("required_fields must contain non-empty strings up to 64 characters")
			}
			if _, duplicate := seen[field]; duplicate {
				continue
			}
			seen[field] = struct{}{}
			acceptance.RequiredFields = append(acceptance.RequiredFields, field)
		}
	}
	if (len(acceptance.RequiredFields) > 0 || acceptance.MinItems > 0) && outputKind != "records" {
		return nil, errors.New("required_fields and min_items require output_kind records")
	}
	return acceptance, nil
}

func numericInt(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case float64:
		converted := int(value)
		return converted, value == float64(converted)
	default:
		return 0, false
	}
}

func (sm *SubagentManager) runTask(
	ctx context.Context,
	task taskregistry.Record,
	modelOverride string,
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
		ModelOverride:  modelOverride,
		TaskPrompt:     buildSpawnSystemPrompt(task.Task, task.Label),
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
	if cfg.Model == "" {
		cfg.Model = sm.defaultModel
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = sm.maxTokens
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = sm.temperature
	}
	return sm.spawner.SpawnSubTurn(ctx, cfg)
}

func (sm *SubagentManager) updateTask(
	taskID string,
	status taskregistry.Status,
	delivery taskregistry.DeliveryStatus,
	summary string,
	mutate func(*taskregistry.Record),
) error {
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

func NewSubagentTool(manager *SubagentManager) (*SubagentTool, error) {
	if manager == nil {
		return nil, errors.New("subagent manager is required")
	}
	return &SubagentTool{manager: manager}, nil
}

func (t *SubagentTool) Name() string {
	return "subagent"
}

func (t *SubagentTool) Description() string {
	return "Execute a subagent task synchronously and return the result. " +
		"Use this for delegating a bounded task to an independent agent instance, including when a different " +
		"configured model would materially improve quality, speed, or cost. An optional model override applies " +
		"only to the child task, so the parent conversation resumes on its current model. Returns an execution " +
		"summary to the user and full details to the LLM."
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
			"model": modelOverrideParameter(t.manager.models),
			"objective_items": objectiveItemsParameter(
				taskresult.ObjectiveKindResult,
				taskresult.ObjectiveKindExternalAction,
			),
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
	modelOverride, parseErr := parseModelOverride(args["model"], t.manager.models)
	if parseErr != nil {
		return toolshared.ErrorResult(parseErr.Error()).WithError(parseErr)
	}
	objectiveItems, parseErr := parseObjectiveItems(
		args["objective_items"],
		taskresult.ObjectiveKindResult,
		taskresult.ObjectiveKindExternalAction,
	)
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

	result, err := t.manager.spawnSubTurn(ctx, SubTurnConfig{
		ModelOverride:  modelOverride,
		Tools:          nil, // Will inherit from parent via context
		TaskPrompt:     systemPrompt,
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
	modelLine := ""
	if modelOverride != "" {
		modelLine = fmt.Sprintf("\nModel: %s", modelOverride)
	}
	llmContent := fmt.Sprintf("Subagent task completed:\nLabel: %s%s\nResult: %s",
		labelStr, modelLine, result.ForLLM)

	result.ForLLM = llmContent
	result.ForUser = userContent
	result.Control.Async = false
	result.Delivery.Intent = toolshared.DeliveryDefault
	return result
}
