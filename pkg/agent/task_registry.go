package agent

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
)

// taskCoordinator owns process-wide task registries and async-completion
// admission. Runner generations share this state while retaining their own
// delivery wiring.
type taskCoordinator struct {
	registries       sync.Map
	completionClaims sync.Map
	currentConfig    func() *config.Config
	codingProfile    *CodingRuntimeProfile
	interactions     *interactionCoordinator
}

func newTaskCoordinator(
	currentConfig func() *config.Config,
	codingProfile *CodingRuntimeProfile,
	interactions *interactionCoordinator,
) taskCoordinator {
	return taskCoordinator{
		currentConfig: currentConfig,
		codingProfile: codingProfile,
		interactions:  interactions,
	}
}

func (c *taskCoordinator) config() *config.Config {
	if c == nil || c.currentConfig == nil {
		return nil
	}
	return c.currentConfig()
}

func (c *taskCoordinator) registry(workspace string) *taskregistry.Registry {
	if c == nil {
		return nil
	}
	workspace = normalizeRuntimeWorkspace(workspace)
	if workspace == "" {
		return nil
	}
	value, ok := c.registries.Load(workspace)
	if !ok {
		return nil
	}
	registry, _ := value.(*taskregistry.Registry)
	return registry
}

func (c *taskCoordinator) configuredRegistry(workspace string) *taskregistry.Registry {
	if c == nil {
		return nil
	}
	workspace = normalizeRuntimeWorkspace(workspace)
	if workspace == "" {
		return nil
	}
	if registry := c.registry(workspace); registry != nil {
		return registry
	}
	storePath := taskregistry.WorkspaceStorePath(workspace)
	if layout, ok := codingLayoutForWorkspace(c.codingProfile, workspace); ok {
		storePath = layout.StatePaths().TaskRegistryFile
	}
	options := taskregistry.Options{}
	if cfg := c.config(); cfg != nil {
		options = cfg.Tasks.Options()
	}
	return c.loadRegistry(workspace, storePath, options, c.interactions)
}

func (al *AgentLoop) taskRegistryForWorkspace(workspace string) *taskregistry.Registry {
	if al == nil {
		return nil
	}
	workspace = normalizeRuntimeWorkspace(workspace)
	if workspace == "" {
		return nil
	}
	if registry := al.tasks.registry(workspace); registry != nil {
		return registry
	}
	// Task restoration needs the authoritative interaction relation before it
	// can decide which active tasks still have a live durable owner.
	_ = al.interactionRegistryForWorkspace(workspace)
	storePath := taskregistry.WorkspaceStorePath(workspace)
	if layout, ok := al.codingLayoutForWorkspace(workspace); ok {
		storePath = layout.StatePaths().TaskRegistryFile
	}
	registry := al.tasks.loadRegistry(
		workspace,
		storePath,
		al.taskRegistryOptions(),
		&al.interactions,
	)
	if al.codingProfile != nil && registry.LastLoadError() != nil {
		al.runtimeInitErr = fmt.Errorf("load coding task registry: %w", registry.LastLoadError())
	}
	return registry
}

func (c *taskCoordinator) loadRegistry(
	workspace string,
	storePath string,
	options taskregistry.Options,
	interactions *interactionCoordinator,
) *taskregistry.Registry {
	if c == nil {
		return nil
	}
	if registry := c.registry(workspace); registry != nil {
		return registry
	}
	candidate := taskregistry.NewRegistryWithOptions(storePath, options)
	actual, loaded := c.registries.LoadOrStore(workspace, candidate)
	registry, _ := actual.(*taskregistry.Registry)
	if registry == nil {
		registry = candidate
	}
	if !loaded {
		c.reconcileActiveTasksAfterRegistryRestore(workspace, registry, interactions)
		c.reconcilePendingTerminalTaskDelivery(workspace, registry)
		logTaskRegistryStats(workspace, registry)
	}
	return registry
}

func (al *AgentLoop) taskRegistryOptions() taskregistry.Options {
	if al != nil && al.cfg != nil {
		return al.cfg.Tasks.Options()
	}
	return taskregistry.Options{}
}

func logTaskRegistryStats(workspace string, registry *taskregistry.Registry) {
	if registry == nil {
		return
	}
	stats := registry.Stats()
	fields := map[string]any{
		"workspace":                workspace,
		"tasks":                    stats.TaskCount,
		"events":                   stats.EventCount,
		"protected_tasks":          stats.ProtectedTaskCount,
		"snapshot_bytes":           stats.SnapshotBytes,
		"max_snapshot_bytes":       stats.MaxSnapshotBytes,
		"max_records":              stats.MaxRecords,
		"max_events":               stats.MaxEvents,
		"terminal_retention_hours": int(stats.TerminalRetention / time.Hour),
	}
	if stats.OverSnapshotBudget {
		logger.WarnCF(
			"agent",
			"Task registry exceeds snapshot budget; only protected tasks remain",
			fields,
		)
		return
	}
	logger.InfoCF("agent", "Loaded task registry", fields)
}

// TaskRegistryForWorkspace returns the durable task registry shared by agent
// tools and gateway-managed runtimes for the given workspace.
func (al *AgentLoop) TaskRegistryForWorkspace(workspace string) *taskregistry.Registry {
	return al.taskRegistryForWorkspace(workspace)
}

func (c *taskCoordinator) updateDeliveryStatus(
	workspace string,
	taskID string,
	status taskregistry.DeliveryStatus,
	completionID string,
	errorSummary string,
) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || status == "" {
		return
	}
	registry := c.configuredRegistry(workspace)
	if registry == nil {
		return
	}
	_ = registry.Update(taskID, func(rec *taskregistry.Record) {
		if rec.Status == taskregistry.StatusCancelled {
			return
		}
		rec.DeliveryStatus = status
		if strings.TrimSpace(completionID) != "" {
			rec.LastCompletionID = strings.TrimSpace(completionID)
		}
		if status == taskregistry.DeliveryDelivered ||
			status == taskregistry.DeliverySessionQueued ||
			status == taskregistry.DeliveryNotApplicable {
			rec.DeliveredAt = time.Now().UnixMilli()
			rec.DeliveryError = ""
		}
		if strings.TrimSpace(errorSummary) != "" {
			rec.DeliveryError = strings.TrimSpace(errorSummary)
			if strings.TrimSpace(rec.Error) == "" {
				rec.Error = strings.TrimSpace(errorSummary)
			}
		}
	})
}

func (c *taskCoordinator) recordDeliveryDecision(
	workspace string,
	decision AsyncDeliveryDecision,
	completionID string,
	sourceTool string,
) {
	taskID := strings.TrimSpace(decision.TaskID)
	if taskID == "" {
		return
	}
	registry := c.configuredRegistry(workspace)
	if registry == nil {
		return
	}
	_ = registry.AppendEvent(taskID, taskregistry.EventTaskDeliveryDecision, map[string]string{
		"completion_id":  completionID,
		"source_tool":    sourceTool,
		"mode":           string(decision.DeliveryMode),
		"will_user":      boolString(decision.PublishToUser),
		"will_parent":    boolString(decision.QueueParent),
		"parent_handled": boolString(decision.ParentHandled),
		"is_error":       boolString(decision.IsError),
		"content_len":    strconv.Itoa(decision.ContentLen),
		"for_user_len":   strconv.Itoa(decision.ForUserLen),
		"media_count":    strconv.Itoa(decision.MediaCount),
	})
}

func (c *taskCoordinator) deliveryAlreadyHandled(
	workspace string,
	taskID string,
	completionID string,
) bool {
	taskID = strings.TrimSpace(taskID)
	completionID = strings.TrimSpace(completionID)
	if taskID == "" || completionID == "" {
		return false
	}
	registry := c.configuredRegistry(workspace)
	if registry == nil {
		return false
	}
	rec, ok := registry.Get(taskID)
	if !ok || strings.TrimSpace(rec.LastCompletionID) != completionID {
		return false
	}
	switch rec.DeliveryStatus {
	case taskregistry.DeliveryDelivered,
		taskregistry.DeliverySessionQueued,
		taskregistry.DeliveryNotApplicable:
		return true
	default:
		return false
	}
}

func (c *taskCoordinator) reconcilePendingTerminalTaskDelivery(
	workspace string,
	registry *taskregistry.Registry,
) {
	if registry == nil {
		return
	}
	pending := registry.ListPendingTerminalDelivery()
	if len(pending) == 0 {
		return
	}
	now := time.Now().UnixMilli()
	for _, rec := range pending {
		taskID := rec.TaskID
		_ = registry.Update(taskID, func(rec *taskregistry.Record) {
			rec.DeliveryStatus = taskregistry.DeliveryParentMissing
			rec.LastEventAt = now
			if strings.TrimSpace(rec.Error) == "" {
				rec.Error = "pending delivery was not completed before runtime restart/reload"
			}
		})
	}
	logger.WarnCF("agent", "Reconciled stale pending task deliveries",
		map[string]any{
			"workspace": workspace,
			"count":     len(pending),
		})
}

func (c *taskCoordinator) reconcileActiveTasksAfterRegistryRestore(
	workspace string,
	registry *taskregistry.Registry,
	interactions *interactionCoordinator,
) {
	if registry == nil {
		return
	}
	active := registry.ListActive()
	if len(active) == 0 {
		return
	}
	protectedTaskIDs, protectionErr := interactions.activeTaskIDs(workspace)
	if protectionErr != nil {
		logger.WarnCF("agent", "Skipped active task reconciliation because interaction state is unavailable",
			map[string]any{
				"workspace": workspace,
				"error":     protectionErr.Error(),
			})
		return
	}
	reason := "task was still active when the runtime registry was restored; previous runtime owner is no longer alive"
	count, err := registry.MarkActiveLost(reason, protectedTaskIDs)
	if count == 0 {
		return
	}
	logger.WarnCF("agent", "Reconciled active tasks from previous runtime as lost",
		map[string]any{
			"workspace": workspace,
			"count":     count,
			"error":     errString(err),
		})
}

func (c *taskCoordinator) claimCompletion(completionID string) bool {
	if c == nil {
		return false
	}
	completionID = strings.TrimSpace(completionID)
	if completionID == "" {
		return false
	}
	_, loaded := c.completionClaims.LoadOrStore(completionID, struct{}{})
	return !loaded
}

func (c *taskCoordinator) releaseCompletion(completionID string) {
	if c == nil {
		return
	}
	c.completionClaims.Delete(strings.TrimSpace(completionID))
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
