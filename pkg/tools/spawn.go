package tools

import (
	"context"
	"fmt"
	"strings"

	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type SpawnTool struct {
	manager                    *SubagentManager
	allowlistCheck             func(targetAgentID string) bool
	requiresObjectiveChecklist func(targetAgentID string) bool
}

// Compile-time check: SpawnTool implements AsyncExecutor.
var _ toolshared.AsyncExecutor = (*SpawnTool)(nil)

func NewSpawnTool(manager *SubagentManager) *SpawnTool {
	return &SpawnTool{manager: manager}
}

func (t *SpawnTool) Name() string {
	return "spawn"
}

func (t *SpawnTool) Description() string {
	return "Spawn a subagent to handle a task in the background. Use this for complex or time-consuming tasks that can run independently. The subagent will complete the task and report back when done. Optional delivery_mode controls whether the final async result goes to the user, the parent agent, or both."
}

func (t *SpawnTool) Parameters() map[string]any {
	props := map[string]any{
		"task": map[string]any{
			"type":        "string",
			"description": "The task for subagent to complete",
		},
		"label": map[string]any{
			"type":        "string",
			"description": "Optional short label for the task (for display)",
		},
		"agent_id": map[string]any{
			"type":        "string",
			"description": "Optional target agent ID to delegate the task to",
		},
		"delivery_mode": map[string]any{
			"type":        "string",
			"description": "Optional async result routing policy: user_only, parent_only, or user_and_parent. Defaults to user_only.",
			"enum": []string{
				string(toolshared.AsyncDeliveryUserOnly),
				string(toolshared.AsyncDeliveryParentOnly),
				string(toolshared.AsyncDeliveryUserAndParent),
			},
		},
		"objective_items": objectiveItemsParameter(),
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   []string{"task"},
	}
}

func (t *SpawnTool) SetAllowlistChecker(check func(targetAgentID string) bool) {
	t.allowlistCheck = check
}

// SetObjectiveChecklistRequirement configures the target-aware preflight used
// to keep browser-capable tasks in the parent turn when their verification
// contract is missing. This lets the caller repair the tool call instead of
// delivering a terminal failure from an already-started background task.
func (t *SpawnTool) SetObjectiveChecklistRequirement(check func(targetAgentID string) bool) {
	t.requiresObjectiveChecklist = check
}

func (t *SpawnTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	return t.execute(ctx, args, nil)
}

// ExecuteAsync implements AsyncExecutor. The callback is passed through to the
// subagent manager as a call parameter — never stored on the SpawnTool instance.
func (t *SpawnTool) ExecuteAsync(
	ctx context.Context,
	args map[string]any,
	cb toolshared.AsyncCallback,
) *toolshared.ToolResult {
	return t.execute(ctx, args, cb)
}

func (t *SpawnTool) execute(
	ctx context.Context,
	args map[string]any,
	cb toolshared.AsyncCallback,
) *toolshared.ToolResult {
	task, ok := args["task"].(string)
	if !ok || strings.TrimSpace(task) == "" {
		return toolshared.ErrorResult("task is required and must be a non-empty string")
	}

	label, ok := args["label"].(string)
	if !ok {
		label = ""
	}
	agentID, ok := args["agent_id"].(string)
	if !ok {
		agentID = ""
	}
	targetAgentID := strings.TrimSpace(agentID)
	deliveryMode, err := parseSpawnDeliveryMode(args["delivery_mode"])
	if err != nil {
		return toolshared.ErrorResult(err.Error()).WithError(err)
	}
	objectiveItems, err := parseObjectiveItems(args["objective_items"])
	if err != nil {
		return toolshared.ErrorResult(err.Error()).WithError(err)
	}

	// Check allowlist if targeting a specific agent
	if targetAgentID != "" && t.allowlistCheck != nil {
		if !t.allowlistCheck(targetAgentID) {
			return toolshared.ErrorResult(fmt.Sprintf("not allowed to spawn agent '%s'", targetAgentID))
		}
	}
	if t.requiresObjectiveChecklist != nil && t.requiresObjectiveChecklist(targetAgentID) && len(objectiveItems) == 0 {
		return toolshared.ErrorResult(
			"objective_items is required when spawning a browser-capable agent; " +
				"retry spawn with every requested result or external action declared",
		)
	}

	// Route through SubagentManager so admission and completion share the
	// canonical task registry.
	if t.manager != nil {
		wrappedCallback := cb
		if cb != nil {
			wrappedCallback = func(cbCtx context.Context, res *toolshared.ToolResult) {
				if res != nil {
					res.WithAsyncDelivery(deliveryMode)
				}
				cb(cbCtx, res)
			}
		}
		ack, err := t.manager.Spawn(
			ctx,
			task,
			label,
			strings.TrimSpace(agentID),
			toolshared.ToolChannel(ctx),
			toolshared.ToolChatID(ctx),
			deliveryMode,
			wrappedCallback,
			objectiveItems,
		)
		if err != nil {
			return toolshared.ErrorResult(fmt.Sprintf("Spawn failed: %v", err)).WithError(err)
		}
		return toolshared.AsyncResult(ack)
	}

	// Fallback: manager not configured
	return toolshared.ErrorResult("Subagent manager not configured")
}

func parseSpawnDeliveryMode(raw any) (toolshared.AsyncDeliveryMode, error) {
	if raw == nil {
		return toolshared.AsyncDeliveryUserOnly, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("delivery_mode must be a string")
	}
	switch toolshared.AsyncDeliveryMode(strings.TrimSpace(value)) {
	case toolshared.AsyncDeliveryUserOnly, toolshared.AsyncDeliveryParentOnly, toolshared.AsyncDeliveryUserAndParent:
		return toolshared.AsyncDeliveryMode(strings.TrimSpace(value)), nil
	case "":
		return toolshared.AsyncDeliveryUserOnly, nil
	default:
		return "", fmt.Errorf("delivery_mode must be one of: user_only, parent_only, user_and_parent")
	}
}

func buildSpawnSystemPrompt(task, label string) string {
	if label != "" {
		return fmt.Sprintf(
			`You are a spawned subagent labeled "%s" running in the background. Complete the given task independently and report back when done.

Task: %s`,
			label,
			task,
		)
	}
	return fmt.Sprintf(
		`You are a spawned subagent running in the background. Complete the given task independently and report back when done.
Task: %s`,
		task,
	)
}
