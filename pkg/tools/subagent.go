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
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
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

type SubagentTask struct {
	ID                  string
	Task                string
	Label               string
	AgentID             string
	OwnerKey            string
	RequesterSessionKey string
	HistoryPolicyKnown  bool
	HistoryDisabled     bool
	OriginChannel       string
	OriginChatID        string
	DeliveryMode        toolshared.AsyncDeliveryMode
	Status              string
	Result              string
	Created             int64
	ObjectiveItems      []toolshared.ObjectiveSpec
}

type SpawnSubTurnFunc func(
	ctx context.Context,
	taskID string,
	task, label, agentID string,
	objectiveItems []toolshared.ObjectiveSpec,
	tools *ToolRegistry,
	maxTokens int,
	temperature float64,
	hasMaxTokens, hasTemperature bool,
) (*toolshared.ToolResult, error)

type SubagentManager struct {
	tasks          map[string]*SubagentTask
	mu             sync.RWMutex
	provider       providers.LLMProvider
	defaultModel   string
	workspace      string
	tools          *ToolRegistry
	maxIterations  int
	maxTokens      int
	temperature    float64
	hasMaxTokens   bool
	hasTemperature bool
	spawner        SpawnSubTurnFunc
	taskRegistry   *taskregistry.Registry

	// mediaResolver resolves media:// refs in tool-loop messages before
	// each LLM call in the legacy RunToolLoop fallback path.
	// This lets subagents reuse the same media handling behavior as the
	// main agent loop without importing pkg/agent and creating a cycle.
	mediaResolver func([]providers.Message) []providers.Message
	loopDetection loopguard.Config
}

// NewSubagentManagerWithRegistry requires the canonical task registry shared
// by every manager that owns the same workspace.
func NewSubagentManagerWithRegistry(
	provider providers.LLMProvider,
	defaultModel, workspace string,
	registry *taskregistry.Registry,
) *SubagentManager {
	manager := &SubagentManager{
		tasks:         make(map[string]*SubagentTask),
		provider:      provider,
		defaultModel:  defaultModel,
		workspace:     workspace,
		tools:         NewToolRegistry(),
		maxIterations: 10,
		loopDetection: loopguard.DefaultConfig(),
		taskRegistry:  registry,
	}
	manager.restoreTasksFromRegistry()
	return manager
}

func (sm *SubagentManager) SetLoopDetection(config loopguard.Config) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.loopDetection = config.Normalized()
}

func (sm *SubagentManager) SetSpawner(spawner SpawnSubTurnFunc) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.spawner = spawner
}

// SetMediaResolver injects a message preprocessor that resolves media:// refs
// into LLM-ready content before each tool-loop iteration.
// This is only used by the legacy RunToolLoop fallback path.
func (sm *SubagentManager) SetMediaResolver(
	resolver func([]providers.Message) []providers.Message,
) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.mediaResolver = resolver
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

// SetTools sets the tool registry for subagent execution.
// If not set, subagent will have access to the provided tools.
func (sm *SubagentManager) SetTools(tools *ToolRegistry) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.tools = tools
}

// RegisterTool registers a tool for subagent execution.
func (sm *SubagentManager) RegisterTool(tool toolshared.Tool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.tools.Register(tool)
}

func (sm *SubagentManager) Spawn(
	ctx context.Context,
	task, label, agentID, originChannel, originChatID string,
	deliveryMode toolshared.AsyncDeliveryMode,
	callback toolshared.AsyncCallback,
	objectiveSets ...[]toolshared.ObjectiveSpec,
) (string, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	taskID := "subagent-" + uuid.NewString()
	var objectiveItems []toolshared.ObjectiveSpec
	if len(objectiveSets) > 0 {
		objectiveItems = objectiveSets[0]
	}
	subagentTask := &SubagentTask{
		ID:                  taskID,
		Task:                task,
		Label:               label,
		AgentID:             agentID,
		OwnerKey:            toolshared.ToolAgentID(ctx),
		RequesterSessionKey: toolshared.ToolSessionKey(ctx),
		HistoryPolicyKnown:  true,
		HistoryDisabled:     toolshared.ToolHistoryDisabled(ctx),
		OriginChannel:       originChannel,
		OriginChatID:        originChatID,
		DeliveryMode:        deliveryMode,
		Status:              "running",
		Created:             time.Now().UnixMilli(),
		ObjectiveItems:      append([]toolshared.ObjectiveSpec(nil), objectiveItems...),
	}
	if err := sm.createTask(subagentTask); err != nil {
		return "", fmt.Errorf("persist spawned subagent: %w", err)
	}
	sm.tasks[taskID] = subagentTask

	// Start task in background with context cancellation support
	go sm.runTask(ctx, subagentTask, callback)

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
	task *SubagentTask,
	callback toolshared.AsyncCallback,
) {
	task.Status = "running"
	// TODO(eventbus): once subagents are modeled as child turns inside
	// pkg/agent, emit SubTurnEnd and SubTurnResultDelivered from the parent
	// AgentLoop instead of this legacy manager.

	// Check if context is already canceled before starting
	select {
	case <-ctx.Done():
		sm.mu.Lock()
		task.Status = "canceled"
		task.Result = "Task canceled before execution"
		sm.mu.Unlock()
		sm.recordTaskOrLog(
			task,
			taskregistry.StatusCancelled,
			taskregistry.DeliveryNotApplicable,
			task.Result,
		)
		return
	default:
	}

	sm.mu.RLock()
	spawner := sm.spawner
	tools := sm.tools
	maxIter := sm.maxIterations
	maxTokens := sm.maxTokens
	temperature := sm.temperature
	hasMaxTokens := sm.hasMaxTokens
	hasTemperature := sm.hasTemperature
	mediaResolver := sm.mediaResolver
	loopDetection := sm.loopDetection
	sm.mu.RUnlock()

	var result *toolshared.ToolResult
	var err error
	stopHeartbeat := startTaskRegistryHeartbeat(ctx, sm.taskRegistry, task.ID, "spawned subagent is still running")
	defer stopHeartbeat()

	if spawner != nil {
		result, err = spawner(
			ctx,
			task.ID,
			task.Task,
			task.Label,
			task.AgentID,
			task.ObjectiveItems,
			tools,
			maxTokens,
			temperature,
			hasMaxTokens,
			hasTemperature,
		)
	} else {
		// Fallback to legacy RunToolLoop
		systemPrompt := `You are a subagent. Complete the given task independently and report the result.
You have access to tools - use them as needed to complete your task.
After completing the task, provide a clear summary of what was done.`

		messages := []providers.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: task.Task},
		}

		var llmOptions map[string]any
		if hasMaxTokens || hasTemperature {
			llmOptions = map[string]any{}
			if hasMaxTokens {
				llmOptions["max_tokens"] = maxTokens
			}
			if hasTemperature {
				llmOptions["temperature"] = temperature
			}
		}

		var loopResult *ToolLoopResult
		loopResult, err = RunToolLoop(ctx, ToolLoopConfig{
			Provider:      sm.provider,
			Model:         sm.defaultModel,
			Tools:         tools,
			MaxIterations: maxIter,
			LLMOptions:    llmOptions,
			MediaResolver: mediaResolver,
			LoopDetection: loopDetection,
		}, messages, task.OriginChannel, task.OriginChatID)

		if err == nil {
			result = &toolshared.ToolResult{
				ForLLM: fmt.Sprintf(
					"Subagent '%s' completed (iterations: %d): %s",
					task.Label,
					loopResult.Iterations,
					loopResult.Content,
				),
				ForUser: loopResult.Content,
				Silent:  false,
				IsError: false,
				Async:   false,
			}
		}
	}
	if result != nil && result.TaskSuspended {
		return
	}

	sm.mu.Lock()
	defer func() {
		sm.mu.Unlock()
		// Call callback if provided and result is set
		if callback != nil && result != nil {
			result.WithAsyncTaskID(task.ID)
			callback(ctx, result)
		}
	}()

	if err != nil {
		task.Status = "failed"
		task.Result = fmt.Sprintf("Error: %v", err)
		// Only report cancellation when cancellation is the actual cause.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			task.Status = "canceled"
			task.Result = "Task canceled during execution"
			sm.recordTaskOrLog(
				task,
				taskregistry.StatusCancelled,
				taskregistry.DeliveryPending,
				task.Result,
			)
		} else {
			sm.recordTaskOrLog(
				task,
				taskregistry.StatusFailed,
				taskregistry.DeliveryPending,
				task.Result,
			)
		}
		result = &toolshared.ToolResult{
			ForLLM:  task.Result,
			ForUser: task.Result,
			Silent:  false,
			IsError: true,
			Async:   false,
			Err:     err,
		}
	} else {
		terminalStatus := terminalTaskStatusForResult(result)
		if terminalStatus == taskregistry.StatusFailed {
			task.Status = "failed"
		} else {
			task.Status = "completed"
		}
		result.WithAsyncTaskID(task.ID)
		task.Result = result.ForLLM
		sm.recordTaskResult(task, result)
	}
}

func (sm *SubagentManager) restoreTasksFromRegistry() {
	if sm == nil || sm.taskRegistry == nil {
		return
	}
	for _, rec := range sm.taskRegistry.List() {
		if rec.Runtime != taskregistry.RuntimeSubagent {
			continue
		}
		sm.tasks[rec.TaskID] = subagentTaskFromRecord(rec)
	}
}

func subagentTaskFromRecord(rec taskregistry.Record) *SubagentTask {
	status := "running"
	switch rec.Status {
	case taskregistry.StatusSucceeded:
		status = "completed"
	case taskregistry.StatusFailed:
		status = "failed"
	case taskregistry.StatusCancelled, taskregistry.StatusTimedOut:
		status = "canceled"
	case taskregistry.StatusRunning, taskregistry.StatusQueued:
		status = "running"
	case taskregistry.StatusWaitingForInput:
		status = "waiting_for_input"
	}
	return &SubagentTask{
		ID:                  rec.TaskID,
		Task:                rec.Task,
		Label:               rec.Label,
		AgentID:             rec.AgentID,
		OwnerKey:            rec.OwnerKey,
		RequesterSessionKey: rec.RequesterSessionKey,
		HistoryPolicyKnown:  rec.HistoryPolicyKnown,
		HistoryDisabled:     rec.HistoryDisabled,
		OriginChannel:       rec.Channel,
		OriginChatID:        rec.ChatID,
		DeliveryMode:        toolshared.AsyncDeliveryMode(rec.DeliveryMode),
		Status:              status,
		Result:              rec.TerminalSummary,
		Created:             rec.CreatedAt,
	}
}

func (sm *SubagentManager) createTask(task *SubagentTask) error {
	if sm == nil || sm.taskRegistry == nil || task == nil {
		return errors.New("subagent task registry is unavailable")
	}
	return sm.taskRegistry.Create(sm.taskRecord(
		task,
		taskregistry.StatusRunning,
		taskregistry.DeliveryPending,
		"",
	))
}

func (sm *SubagentManager) recordTask(
	task *SubagentTask,
	status taskregistry.Status,
	delivery taskregistry.DeliveryStatus,
	summary string,
) error {
	return sm.updateTask(task, status, delivery, summary, nil)
}

func (sm *SubagentManager) updateTask(
	task *SubagentTask,
	status taskregistry.Status,
	delivery taskregistry.DeliveryStatus,
	summary string,
	mutate func(*taskregistry.Record),
) error {
	if sm == nil || sm.taskRegistry == nil || task == nil {
		return errors.New("subagent task registry is unavailable")
	}
	rec := sm.taskRecord(task, status, delivery, summary)
	return sm.taskRegistry.Update(task.ID, func(stored *taskregistry.Record) {
		stored.Runtime = rec.Runtime
		stored.TaskKind = rec.TaskKind
		stored.Channel = rec.Channel
		stored.ChatID = rec.ChatID
		stored.AgentID = rec.AgentID
		stored.OwnerKey = rec.OwnerKey
		stored.RequesterSessionKey = rec.RequesterSessionKey
		stored.HistoryPolicyKnown = rec.HistoryPolicyKnown
		stored.HistoryDisabled = rec.HistoryDisabled
		stored.Label = rec.Label
		stored.Task = rec.Task
		stored.Status = rec.Status
		stored.DeliveryStatus = rec.DeliveryStatus
		stored.NotifyPolicy = rec.NotifyPolicy
		stored.DeliveryMode = rec.DeliveryMode
		stored.EndedAt = rec.EndedAt
		stored.LastEventAt = rec.LastEventAt
		stored.Error = rec.Error
		stored.TerminalSummary = rec.TerminalSummary
		if mutate != nil {
			mutate(stored)
		}
	})
}

func (sm *SubagentManager) taskRecord(
	task *SubagentTask,
	status taskregistry.Status,
	delivery taskregistry.DeliveryStatus,
	summary string,
) taskregistry.Record {
	now := time.Now().UnixMilli()
	rec := taskregistry.Record{
		TaskID:              task.ID,
		Runtime:             taskregistry.RuntimeSubagent,
		TaskKind:            "spawn",
		Channel:             task.OriginChannel,
		ChatID:              task.OriginChatID,
		AgentID:             task.AgentID,
		OwnerKey:            task.OwnerKey,
		RequesterSessionKey: task.RequesterSessionKey,
		HistoryPolicyKnown:  task.HistoryPolicyKnown,
		HistoryDisabled:     task.HistoryDisabled,
		Label:               task.Label,
		Task:                task.Task,
		Status:              status,
		DeliveryStatus:      delivery,
		NotifyPolicy:        taskregistry.NotifyDoneOnly,
		DeliveryMode:        string(task.DeliveryMode),
		CreatedAt:           task.Created,
		StartedAt:           task.Created,
		LastEventAt:         now,
	}
	if rec.CreatedAt == 0 {
		rec.CreatedAt = now
	}
	if rec.StartedAt == 0 {
		rec.StartedAt = rec.CreatedAt
	}
	if status == taskregistry.StatusSucceeded || status == taskregistry.StatusFailed ||
		status == taskregistry.StatusCancelled ||
		status == taskregistry.StatusTimedOut {
		rec.EndedAt = now
		rec.TerminalSummary = summary
	}
	if status == taskregistry.StatusFailed {
		rec.Error = summary
	}
	return rec
}

func (sm *SubagentManager) recordTaskOrLog(
	task *SubagentTask,
	status taskregistry.Status,
	delivery taskregistry.DeliveryStatus,
	summary string,
) {
	if err := sm.recordTask(task, status, delivery, summary); err != nil {
		logger.WarnCF("subagent", "Failed to persist subagent task state", map[string]any{
			"task_id": task.ID,
			"status":  status,
			"error":   err.Error(),
		})
	}
}

func (sm *SubagentManager) recordTaskResult(task *SubagentTask, result *toolshared.ToolResult) {
	if sm == nil || sm.taskRegistry == nil || task == nil {
		return
	}
	summary := ""
	if result != nil {
		summary = result.ContentForLLM()
	}
	delivery := taskregistry.DeliveryPending
	if result == nil || (result.Silent && result.AsyncDelivery == toolshared.AsyncDeliveryParentOnly) {
		delivery = taskregistry.DeliveryNotApplicable
	}
	deliverable := taskDeliverable(result)
	if err := sm.updateTask(
		task,
		terminalTaskStatusForResult(result),
		delivery,
		summary,
		func(rec *taskregistry.Record) {
			rec.Deliverable = deliverable
		},
	); err != nil {
		logger.WarnCF("subagent", "Failed to persist subagent task result", map[string]any{
			"task_id": task.ID,
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

func (sm *SubagentManager) GetTask(taskID string) (*SubagentTask, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	task, ok := sm.tasks[taskID]
	return task, ok
}

// GetTaskCopy returns a copy of the task with the given ID, taken under the
// read lock, so the caller receives a consistent snapshot with no data race.
func (sm *SubagentManager) GetTaskCopy(taskID string) (SubagentTask, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	task, ok := sm.tasks[taskID]
	if !ok {
		return SubagentTask{}, false
	}
	return *task, true
}

func (sm *SubagentManager) ListTasks() []*SubagentTask {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	tasks := make([]*SubagentTask, 0, len(sm.tasks))
	for _, task := range sm.tasks {
		tasks = append(tasks, task)
	}
	return tasks
}

// ListTaskCopies returns value copies of all tasks, taken under the read lock,
// so callers receive consistent snapshots with no data race.
func (sm *SubagentManager) ListTaskCopies() []SubagentTask {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	copies := make([]SubagentTask, 0, len(sm.tasks))
	for _, task := range sm.tasks {
		copies = append(copies, *task)
	}
	return copies
}

// SubagentTool executes a subagent task synchronously and returns the result.
// It directly calls SubTurnSpawner with Async=false for synchronous execution.
type SubagentTool struct {
	spawner      SubTurnSpawner
	defaultModel string
	maxTokens    int
	temperature  float64
}

func NewSubagentTool(manager *SubagentManager) *SubagentTool {
	if manager == nil {
		return &SubagentTool{}
	}
	return &SubagentTool{
		defaultModel: manager.defaultModel,
		maxTokens:    manager.maxTokens,
		temperature:  manager.temperature,
	}
}

// SetSpawner sets the SubTurnSpawner for direct sub-turn execution.
func (t *SubagentTool) SetSpawner(spawner SubTurnSpawner) {
	t.spawner = spawner
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

	// Use spawner if available (direct SpawnSubTurn call)
	if t.spawner != nil {
		result, err := t.spawner.SpawnSubTurn(ctx, SubTurnConfig{
			Model:          t.defaultModel,
			Tools:          nil, // Will inherit from parent via context
			SystemPrompt:   systemPrompt,
			MaxTokens:      t.maxTokens,
			Temperature:    t.temperature,
			Async:          false, // Synchronous execution
			ObjectiveItems: objectiveItems,
		})
		if err != nil {
			return toolshared.ErrorResult(fmt.Sprintf("Subagent execution failed: %v", err)).WithError(err)
		}
		if result == nil {
			return toolshared.ErrorResult("Subagent execution returned no result")
		}
		if result.TaskSuspended {
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
		result.Silent = false
		result.Async = false
		return result
	}

	// Fallback: spawner not configured
	return toolshared.ErrorResult("Subagent manager not configured").WithError(fmt.Errorf("spawner not set"))
}
