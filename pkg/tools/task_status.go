package tools

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const taskStatusActiveStaleAfter = 30 * time.Minute

const (
	defaultTaskStatusListLimit = 12
	maxTaskStatusListLimit     = 25
)

// TaskStatusTool reports durable runtime task/run records across spawn,
// delegate, cron, and future background runtimes.
type TaskStatusTool struct {
	registry     *taskregistry.Registry
	interactions *interactions.Registry
}

type taskStatusView struct {
	task        taskregistry.Record
	interaction *interactions.Record
}

func NewTaskStatusTool(
	registry *taskregistry.Registry,
	interactionRegistry *interactions.Registry,
) *TaskStatusTool {
	return &TaskStatusTool{registry: registry, interactions: interactionRegistry}
}

func (t *TaskStatusTool) Name() string {
	return "task_status"
}

func (t *TaskStatusTool) Description() string {
	return "Get durable runtime task status for spawn/delegate/cron/subtask runs. " +
		"Prefer this for general task history, completed task checks, and after service restarts. " +
		"Results are scoped to the current conversation's channel/chat when available. " +
		"Without task_id, returns a compact list of the most recent tasks; use task_id for a full task record."
}

func (t *TaskStatusTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id": map[string]any{
				"type":        "string",
				"description": "Optional durable task ID, e.g. subagent-1 or delegate-...",
			},
			"task_kind": map[string]any{
				"type":        "string",
				"description": "Optional task kind filter, e.g. spawn or delegate.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     maxTaskStatusListLimit,
				"description": "Maximum recent tasks to return in list mode (default 12, maximum 25). Ignored with task_id.",
			},
			"include_events": map[string]any{
				"type":        "boolean",
				"description": "Include typed task event details. With task_id, shows that task's event stream. With list output, shows recent events per visible task.",
			},
			"include_deliverable": map[string]any{
				"type":        "boolean",
				"description": "Return the complete durable deliverable text. Requires an exact task_id. Use this to recover or present a completed task's full result.",
			},
		},
		"required": []string{},
	}
}

func (t *TaskStatusTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	if t == nil || t.registry == nil {
		return toolshared.ErrorResult("task registry not configured")
	}
	activeInteractions := t.activeInteractionsByTask()
	var protectedTaskIDs map[string]struct{}
	if t.interactions != nil {
		if err := t.interactions.LastLoadError(); err != nil {
			return toolshared.ErrorResult(
				fmt.Sprintf("failed to read current interaction state: %v", err),
			).WithError(err)
		}
		protectedTaskIDs = t.interactions.NonterminalTaskIDs()
	}
	if _, err := t.registry.MarkStaleActiveLost(
		taskStatusActiveStaleAfter,
		"active task did not report progress before task_status stale timeout",
		protectedTaskIDs,
	); err != nil {
		return toolshared.ErrorResult(fmt.Sprintf("failed to reconcile stale active tasks: %v", err)).WithError(err)
	}
	taskID, err := optionalTaskStatusStringArg(args, "task_id")
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	taskKind, err := optionalTaskStatusStringArg(args, "task_kind")
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	includeEvents, err := optionalTaskStatusBoolArg(args, "include_events")
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	includeDeliverable, err := optionalTaskStatusBoolArg(args, "include_deliverable")
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	if includeDeliverable && taskID == "" {
		return toolshared.ErrorResult("include_deliverable requires task_id")
	}
	callerChannel := toolshared.ToolChannel(ctx)
	callerChatID := toolshared.ToolChatID(ctx)
	callerTopicID := toolshared.ToolTopicID(ctx)

	if taskID != "" {
		rec, ok := t.registry.Get(taskID)
		if !ok || !taskRecordVisibleToCaller(rec, callerChannel, callerChatID, callerTopicID) {
			return toolshared.ErrorResult(fmt.Sprintf("No task found with task ID: %s", taskID))
		}
		out := formatTaskRecord(taskStatusView{task: rec, interaction: waitingTaskInteraction(rec, activeInteractions)})
		if includeDeliverable {
			out += formatCompleteTaskDeliverable(rec)
		}
		if includeEvents {
			out = out + "\n" + formatTaskEvents(t.registry.ListEvents(taskID))
		}
		return toolshared.NewToolResult(out)
	}
	limit, err := optionalTaskStatusLimitArg(args)
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}

	records := t.registry.List()
	filtered := make([]taskStatusView, 0, len(records))
	for _, rec := range records {
		if taskKind != "" && rec.TaskKind != taskKind {
			continue
		}
		if !taskRecordVisibleToCaller(rec, callerChannel, callerChatID, callerTopicID) {
			continue
		}
		filtered = append(filtered, taskStatusView{
			task:        rec,
			interaction: waitingTaskInteraction(rec, activeInteractions),
		})
	}
	if len(filtered) == 0 {
		if taskKind != "" {
			return toolshared.NewToolResult(fmt.Sprintf("No visible tasks found for task_kind %q.", taskKind))
		}
		return toolshared.NewToolResult("No visible durable tasks are registered for this conversation.")
	}

	slices.SortFunc(filtered, func(a, b taskStatusView) int {
		if c := cmp.Compare(b.task.CreatedAt, a.task.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(b.task.TaskID, a.task.TaskID)
	})

	counts := map[string]int{}
	for _, view := range filtered {
		counts[view.status()]++
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Task status report (%d total):\n", len(filtered))
	for _, status := range []string{
		string(taskregistry.StatusQueued),
		string(taskregistry.StatusRunning),
		"waiting_for_input",
		string(taskregistry.StatusSucceeded),
		string(taskregistry.StatusFailed),
		string(taskregistry.StatusTimedOut),
		string(taskregistry.StatusCancelled),
		string(taskregistry.StatusLost),
	} {
		if n := counts[status]; n > 0 {
			fmt.Fprintf(&sb, "  %-10s %d\n", status+":", n)
		}
	}
	sb.WriteString("\n")
	visible := filtered
	if len(visible) > limit {
		visible = visible[:limit]
	}
	for _, view := range visible {
		sb.WriteString(formatTaskListRecord(view))
		if includeEvents {
			sb.WriteString("\n")
			sb.WriteString(formatRecentTaskEvents(t.registry.ListEvents(view.task.TaskID), 3))
		}
		sb.WriteString("\n")
	}
	if omitted := len(filtered) - len(visible); omitted > 0 {
		fmt.Fprintf(&sb, "... %d older task(s) omitted. Use task_id for a full task record or limit to show more.\n",
			omitted)
	}
	return toolshared.NewToolResult(strings.TrimSpace(sb.String()))
}

func formatCompleteTaskDeliverable(rec taskregistry.Record) string {
	text := ""
	if rec.Deliverable != nil {
		text = rec.Deliverable.Text
	}
	if strings.TrimSpace(text) == "" {
		return "\n\nComplete deliverable: no durable text is available."
	}
	return "\n\nComplete deliverable:\n" + text
}

func optionalTaskStatusLimitArg(args map[string]any) (int, error) {
	const key = "limit"
	raw, ok := args[key]
	if !ok || raw == nil {
		return defaultTaskStatusListLimit, nil
	}

	var limit int
	switch value := raw.(type) {
	case int:
		limit = value
	case int64:
		limit = int(value)
	case float64:
		if value != float64(int(value)) {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		limit = int(value)
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	if limit < 1 || limit > maxTaskStatusListLimit {
		return 0, fmt.Errorf("%s must be between 1 and %d", key, maxTaskStatusListLimit)
	}
	return limit, nil
}

func optionalTaskStatusBoolArg(args map[string]any, key string) (bool, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return false, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return value, nil
}

func optionalTaskStatusStringArg(args map[string]any, key string) (string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return strings.TrimSpace(value), nil
}

func taskRecordVisibleToCaller(rec taskregistry.Record, channel, chatID, topicID string) bool {
	if channel != "" && rec.Channel != "" && rec.Channel != channel {
		return false
	}
	if chatID != "" && rec.ChatID != "" && rec.ChatID != chatID {
		return false
	}
	if topicID != "" && rec.TopicID != "" && rec.TopicID != topicID {
		return false
	}
	return true
}

func formatTaskRecord(view taskStatusView) string {
	rec := view.task
	var sb strings.Builder
	fmt.Fprintf(&sb, "Task %s [%s/%s]\n", rec.TaskID, rec.Runtime, rec.TaskKind)
	fmt.Fprintf(&sb, "  Status: %s\n", view.status())
	fmt.Fprintf(&sb, "  Delivery: %s", rec.DeliveryStatus)
	if rec.DeliveryMode != "" {
		fmt.Fprintf(&sb, " (%s)", rec.DeliveryMode)
	}
	sb.WriteString("\n")
	if rec.LastCompletionID != "" {
		fmt.Fprintf(&sb, "  Completion ID: %s\n", rec.LastCompletionID)
	}
	if rec.DeliveredAt > 0 {
		fmt.Fprintf(&sb, "  Delivered: %s\n", formatTaskTime(rec.DeliveredAt))
	}
	if rec.DeliveryError != "" {
		fmt.Fprintf(&sb, "  Delivery error: %s\n", truncateTaskText(rec.DeliveryError, 500))
	}
	if rec.AgentID != "" {
		fmt.Fprintf(&sb, "  Agent: %s\n", rec.AgentID)
	}
	if rec.Channel != "" || rec.ChatID != "" || rec.TopicID != "" {
		fmt.Fprintf(&sb, "  Scope: %s/%s", rec.Channel, rec.ChatID)
		if rec.TopicID != "" {
			fmt.Fprintf(&sb, " topic=%s", rec.TopicID)
		}
		sb.WriteString("\n")
	}
	if rec.CreatedAt > 0 {
		fmt.Fprintf(&sb, "  Created: %s\n", formatTaskTime(rec.CreatedAt))
	}
	if rec.EndedAt > 0 {
		fmt.Fprintf(&sb, "  Ended: %s\n", formatTaskTime(rec.EndedAt))
	}
	if rec.Task != "" {
		fmt.Fprintf(&sb, "  Task: %s\n", truncateTaskText(rec.Task, 240))
	}
	appendTaskInteractionStatus(&sb, view.interaction, "  ")
	if rec.TerminalSummary != "" {
		fmt.Fprintf(&sb, "  Result: %s\n", truncateTaskText(rec.TerminalSummary, 500))
	}
	if rec.Error != "" {
		fmt.Fprintf(&sb, "  Error: %s\n", truncateTaskText(rec.Error, 500))
	}
	if rec.Deliverable != nil {
		fmt.Fprintf(&sb, "  Deliverable: text=%t artifacts=%d report=%t\n",
			rec.Deliverable.Text != "",
			len(rec.Deliverable.Artifacts),
			rec.Deliverable.Report != nil)
		if rec.Deliverable.Report != nil {
			sb.WriteString(formatDeliverableReport(rec.Deliverable.Report))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func formatTaskListRecord(view taskStatusView) string {
	rec := view.task
	var sb strings.Builder
	fmt.Fprintf(&sb, "Task %s [%s/%s] status=%s delivery=%s",
		rec.TaskID,
		rec.Runtime,
		rec.TaskKind,
		view.status(),
		rec.DeliveryStatus)
	if rec.AgentID != "" {
		fmt.Fprintf(&sb, " agent=%s", rec.AgentID)
	}
	if rec.CreatedAt > 0 {
		fmt.Fprintf(&sb, " created=%s", formatTaskTime(rec.CreatedAt))
	}
	if rec.Task != "" {
		fmt.Fprintf(&sb, "\n  Task: %s", truncateTaskText(rec.Task, 160))
	}
	appendTaskInteractionStatus(&sb, view.interaction, "\n  ")
	if rec.TerminalSummary != "" {
		fmt.Fprintf(&sb, "\n  Result: %s", truncateTaskText(rec.TerminalSummary, 240))
	} else if rec.Error != "" {
		fmt.Fprintf(&sb, "\n  Error: %s", truncateTaskText(rec.Error, 240))
	}
	if rec.Deliverable != nil {
		fmt.Fprintf(&sb, "\n  Deliverable: text=%t artifacts=%d report=%t",
			rec.Deliverable.Text != "",
			len(rec.Deliverable.Artifacts),
			rec.Deliverable.Report != nil)
	}
	return sb.String()
}

func appendTaskInteractionStatus(
	sb *strings.Builder,
	interaction *interactions.Record,
	prefix string,
) {
	if sb == nil || interaction == nil {
		return
	}
	requestID := strings.TrimSpace(interaction.ShortID)
	if requestID == "" {
		requestID = "unknown"
	}
	fmt.Fprintf(sb, "%sInput required: request=%s", prefix, requestID)
	if summary := strings.TrimSpace(interaction.PromptSummary); summary != "" {
		sb.WriteString(" summary=" + truncateTaskText(summary, 240))
	}
	if prefix == "  " {
		sb.WriteString("\n")
	}
}

func (view taskStatusView) status() string {
	if view.interaction != nil {
		return "waiting_for_input"
	}
	return string(view.task.Status)
}

func (t *TaskStatusTool) activeInteractionsByTask() map[string]interactions.Record {
	active := make(map[string]interactions.Record)
	if t == nil || t.interactions == nil {
		return active
	}
	for _, record := range t.interactions.ListNonterminal() {
		taskID := strings.TrimSpace(record.Origin.TaskID)
		if taskID == "" {
			continue
		}
		current, exists := active[taskID]
		if !exists || record.UpdatedAt > current.UpdatedAt ||
			(record.UpdatedAt == current.UpdatedAt && record.ID > current.ID) {
			active[taskID] = record
		}
	}
	return active
}

func waitingTaskInteraction(
	task taskregistry.Record,
	active map[string]interactions.Record,
) *interactions.Record {
	if task.Status != taskregistry.StatusQueued && task.Status != taskregistry.StatusRunning {
		return nil
	}
	record, ok := active[task.TaskID]
	if !ok || (record.Status != interactions.StatusCreated && record.Status != interactions.StatusWaiting) {
		return nil
	}
	return &record
}

func formatDeliverableReport(report *taskresult.Report) string {
	if report == nil {
		return ""
	}
	var sb strings.Builder
	schema := strings.TrimSpace(report.SchemaVersion)
	if schema == "" {
		schema = "unknown"
	}
	fmt.Fprintf(&sb, "  Report: %s", schema)
	if report.ReportID != "" {
		fmt.Fprintf(&sb, " id=%s", truncateTaskText(report.ReportID, 96))
	}
	if report.ContentHash != "" {
		fmt.Fprintf(&sb, " hash=%s", truncateTaskText(report.ContentHash, 12))
	}
	sb.WriteString("\n")
	if report.Summary != "" {
		fmt.Fprintf(&sb, "    Summary: %s\n", truncateTaskText(report.Summary, 280))
	}
	if status := report.Metadata["result_status"]; status != "" {
		fmt.Fprintf(&sb, "    Status: %s\n", status)
	}
	if len(report.Claims) > 0 {
		fmt.Fprintf(&sb, "    Claims: %d\n", len(report.Claims))
		for i, claim := range report.Claims {
			if i >= 3 {
				fmt.Fprintf(&sb, "      ...and %d more\n", len(report.Claims)-i)
				break
			}
			fmt.Fprintf(&sb, "      - %s\n", formatReportClaim(claim))
		}
	}
	if len(report.FieldDeltas) > 0 {
		fmt.Fprintf(&sb, "    Field deltas: %d\n", len(report.FieldDeltas))
	}
	return sb.String()
}

func formatReportClaim(claim taskresult.Claim) string {
	kind := strings.TrimSpace(claim.Kind)
	if kind == "" {
		kind = "claim"
	}
	text := truncateTaskText(claim.Text, 220)
	if claim.Confidence != "" {
		return fmt.Sprintf("%s [%s]: %s", kind, claim.Confidence, text)
	}
	return fmt.Sprintf("%s: %s", kind, text)
}

func formatTaskEvents(events []taskregistry.TaskEvent) string {
	if len(events) == 0 {
		return "Events: none"
	}
	var sb strings.Builder
	sb.WriteString("Events:\n")
	for _, evt := range events {
		sb.WriteString("  ")
		sb.WriteString(formatTaskEventLine(evt))
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func formatRecentTaskEvents(events []taskregistry.TaskEvent, limit int) string {
	if len(events) == 0 {
		return "  Recent events: none"
	}
	if limit <= 0 || limit > len(events) {
		limit = len(events)
	}
	start := len(events) - limit
	var sb strings.Builder
	sb.WriteString("  Recent events:\n")
	for _, evt := range events[start:] {
		sb.WriteString("  ")
		sb.WriteString(formatTaskEventLine(evt))
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func formatTaskEventLine(evt taskregistry.TaskEvent) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "#%d %s runtime=%s producer=%s source=%s status=%s delivery=%s at=%s",
		evt.Seq,
		evt.Type,
		evt.Runtime,
		firstNonEmptyTaskStatus(evt.Producer, "unknown"),
		firstNonEmptyTaskStatus(evt.Source, "unknown"),
		evt.Status,
		evt.DeliveryStatus,
		formatTaskTime(evt.EmittedAt))
	if payloadKind := strings.TrimSpace(evt.Payload["payload_kind"]); payloadKind != "" {
		fmt.Fprintf(&sb, " payload_kind=%s", payloadKind)
	}
	deliveryMode := firstNonEmptyTaskStatus(evt.Payload["delivery_mode"], evt.Payload["mode"])
	if deliveryMode != "" {
		fmt.Fprintf(&sb, " delivery_mode=%s", deliveryMode)
	}
	if completionID := strings.TrimSpace(evt.Payload["completion_id"]); completionID != "" {
		fmt.Fprintf(&sb, " completion_id=%s", completionID)
	}
	if evt.Fingerprint != "" {
		fmt.Fprintf(&sb, " fingerprint=%s", truncateTaskText(evt.Fingerprint, 12))
	}
	if len(evt.Payload) > 0 {
		fmt.Fprintf(&sb, " payload=%s", formatTaskEventPayload(evt.Payload))
	}
	return sb.String()
}

func formatTaskEventPayload(payload map[string]string) string {
	if len(payload) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", key, payload[key]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func firstNonEmptyTaskStatus(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func formatTaskTime(ms int64) string {
	return time.UnixMilli(ms).Format(time.RFC3339)
}

func truncateTaskText(s string, limit int) string {
	s = strings.TrimSpace(s)
	if limit <= 0 || len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}
