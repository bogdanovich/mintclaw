package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/session"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
)

type interactionControlSyncManager interface {
	SyncInteractionControls(bus.OutboundMessage) error
}

func (al *AgentLoop) scheduleHumanInteractionRecovery(ctx context.Context) {
	if al == nil {
		return
	}
	go al.RecoverHumanInteractions(ctx)
}

// RecoverHumanInteractions retries prompt delivery, claims timeouts, and
// resumes answers whose durable owner disappeared during restart or reload.
func (al *AgentLoop) RecoverHumanInteractions(ctx context.Context) int {
	if al == nil || !al.interactionRecoveryRunning.CompareAndSwap(false, true) {
		return 0
	}
	defer al.interactionRecoveryRunning.Store(false)
	al.loadCatalogedInteractionRegistries()
	recovered := 0
	al.interactionRegistries.Range(func(key, value any) bool {
		if ctx.Err() != nil {
			return false
		}
		workspace, _ := key.(string)
		registry, _ := value.(*interactions.Registry)
		if registry == nil {
			return true
		}
		if claimed, err := registry.ClaimOverdue(time.Now()); err != nil {
			logger.WarnCF("agent", "Failed to claim overdue interactions", map[string]any{
				"workspace": workspace, "error": err.Error(),
			})
		} else if len(claimed) > 0 {
			logger.InfoCF("agent", "Claimed overdue human interactions", map[string]any{
				"workspace": workspace, "count": len(claimed),
			})
		}
		for _, record := range registry.ListNonterminal() {
			if ctx.Err() != nil {
				return false
			}
			if !al.interactionAgentAvailable(workspace, record) {
				if failed, err := registry.Fail(
					record.ID,
					record.Revision,
					"agent_unavailable",
					"the originating agent or workspace is no longer configured",
				); err == nil {
					al.syncInteractionControls(workspace, failed, bus.OutboundInteractionControlsRemove)
					recovered++
				}
				continue
			}
			switch record.Status {
			case interactions.StatusCreated:
				if al.recoverInteractionPrompt(ctx, workspace, registry, record) {
					recovered++
				}
			case interactions.StatusWaiting:
				al.syncInteractionControls(workspace, record, bus.OutboundInteractionControlsPrompt)
			case interactions.StatusResuming, interactions.StatusClaimed:
				if al.recoverClaimedInteraction(ctx, workspace, record) {
					recovered++
				}
			case interactions.StatusCanceling:
				al.syncInteractionControls(workspace, record, bus.OutboundInteractionControlsRemove)
				if al.recoverCancelingInteraction(ctx, workspace, registry, record) {
					recovered++
				}
			}
		}
		al.interactionCatalogMu.Lock()
		pruneErr := registry.Prune(time.Now())
		if pruneErr != nil {
			logger.WarnCF("agent", "Failed to prune human interaction registry", map[string]any{
				"workspace": workspace,
				"error":     pruneErr.Error(),
			})
		}
		if pruneErr == nil && registry.LastLoadError() == nil &&
			registry.Stats().RecordCount == 0 && al.interactionCatalog != nil {
			if err := al.interactionCatalog.Remove(workspace); err != nil {
				logger.WarnCF("agent", "Failed to remove empty interaction workspace", map[string]any{
					"workspace": workspace,
					"error":     err.Error(),
				})
			}
		}
		al.interactionCatalogMu.Unlock()
		return true
	})
	return recovered
}

func (al *AgentLoop) syncInteractionControls(workspace string, record interactions.Record, controls string) {
	if (record.Kind != interactions.KindQuestion && record.Kind != interactions.KindApproval) ||
		al.channelManager == nil {
		return
	}
	syncer, ok := al.channelManager.(interactionControlSyncManager)
	if !ok {
		return
	}
	message := interactionPromptMessage(record)
	interactionKind := bus.OutboundInteractionQuestion
	if record.Kind == interactions.KindApproval {
		interactionKind = bus.OutboundInteractionApproval
	}
	bus.OutboundMetadata{
		InteractionKind:     interactionKind,
		InteractionControls: controls,
	}.ApplyToContext(&message.Context)
	if err := syncer.SyncInteractionControls(message); err != nil {
		logger.WarnCF("agent", "Failed to sync human interaction controls", map[string]any{
			"workspace":      workspace,
			"interaction_id": record.ID,
			"channel":        record.Route.Channel,
			"controls":       controls,
			"error":          err.Error(),
		})
	}
}

func (al *AgentLoop) recoverPromptDeliveryExhaustion(
	ctx context.Context,
	workspace string,
	registry *interactions.Registry,
	record interactions.Record,
) bool {
	agentRegistry := al.GetRegistry()
	if agentRegistry == nil {
		return false
	}
	agent, ok := agentRegistry.GetAgent(record.Route.AgentID)
	if !ok || agent == nil || (record.Origin.TaskID == "" &&
		strings.TrimSpace(agent.Workspace) != strings.TrimSpace(workspace)) {
		return false
	}
	routeSessionKey := record.Route.RouteSessionKey
	if routeSessionKey == "" {
		routeSessionKey = record.Route.SessionKey
	}
	target := &inboundDispatchTarget{
		Agent:         agent,
		RouteClaimKey: runtimeRouteClaimKey(routeSessionKey, ""),
		Allocation: session.Allocation{
			RouteScopeKey: routeSessionKey,
			SessionKey:    record.Route.SessionKey,
		},
		SessionKey: record.Route.SessionKey,
	}
	claim, _, claimed := al.claimRuntimeRouteSession(
		target,
		fmt.Sprintf("pending-interaction-prompt-exhaustion-%s-%d", record.ShortID, al.turnSeq.Add(1)),
	)
	if !claimed {
		return false
	}
	defer claim.releaseIfOwned()
	const failureCode = "prompt_delivery_exhausted"
	if err := al.ensureInteractionCancellationToolResult(
		ctx,
		al.interactionContinuationAgent(record, agent),
		record,
		failureCode,
	); err != nil {
		return false
	}
	if !al.failRecoveredInteraction(
		ctx,
		workspace,
		registry,
		record,
		failureCode,
		"prompt delivery exhausted its bounded retry budget",
	) {
		return false
	}
	_ = al.drainDeferredInteractionIngress(
		ctx,
		workspace,
		record.Route,
		inboundContextForInteraction(record.Route),
	)
	return true
}

func (al *AgentLoop) failRecoveredInteraction(
	ctx context.Context,
	workspace string,
	registry *interactions.Registry,
	record interactions.Record,
	code string,
	detail string,
) bool {
	if taskID := strings.TrimSpace(record.Origin.TaskID); taskID != "" {
		tasks := al.taskRegistryForWorkspace(workspace)
		if tasks == nil {
			return false
		}
		if err := tasks.FinishInteraction(
			taskID,
			record.ID,
			taskregistry.StatusFailed,
			detail,
		); err != nil {
			logger.WarnCF("agent", "Failed to persist recovered interaction task failure", map[string]any{
				"workspace": workspace, "task_id": taskID,
				"interaction_id": record.ID, "error": err.Error(),
			})
			return false
		}
	}
	failed, err := registry.Fail(record.ID, record.Revision, code, detail)
	if err != nil {
		return false
	}
	al.syncInteractionControls(workspace, failed, bus.OutboundInteractionControlsRemove)
	if agents := al.GetRegistry(); agents != nil {
		if agent, ok := agents.GetAgent(record.Route.AgentID); ok {
			al.cleanupInteractionOriginTools(ctx, agent, failed)
		}
	}
	return true
}

func (al *AgentLoop) loadCatalogedInteractionRegistries() {
	if al == nil || al.interactionCatalog == nil {
		return
	}
	workspaces, err := al.interactionCatalog.List()
	if err != nil {
		logger.WarnCF("agent", "Interaction workspace catalog has invalid entries", map[string]any{
			"error": err.Error(),
		})
	}
	for _, workspace := range workspaces {
		_ = al.interactionRegistryForWorkspace(workspace)
	}
}

func (al *AgentLoop) interactionAgentAvailable(
	workspace string,
	record interactions.Record,
) bool {
	if al == nil {
		return false
	}
	registry := al.GetRegistry()
	if registry == nil {
		// Isolated runtimes can reconcile store-only transitions without an
		// agent registry. Production loops always provide one.
		return true
	}
	agent, ok := registry.GetAgent(record.Route.AgentID)
	if !ok || agent == nil || !registry.hasWorkspace(workspace) {
		return false
	}
	return record.Origin.TaskID != "" ||
		normalizeRuntimeWorkspace(agent.Workspace) == normalizeRuntimeWorkspace(workspace)
}

func (al *AgentLoop) recoverCancelingInteraction(
	ctx context.Context,
	workspace string,
	registry *interactions.Registry,
	record interactions.Record,
) bool {
	agentRegistry := al.GetRegistry()
	if agentRegistry == nil {
		return false
	}
	agent, ok := agentRegistry.GetAgent(record.Route.AgentID)
	if !ok || agent == nil || (record.Origin.TaskID == "" &&
		strings.TrimSpace(agent.Workspace) != strings.TrimSpace(workspace)) {
		return false
	}
	routeSessionKey := record.Route.RouteSessionKey
	if routeSessionKey == "" {
		routeSessionKey = record.Route.SessionKey
	}
	target := &inboundDispatchTarget{
		Agent:         agent,
		RouteClaimKey: runtimeRouteClaimKey(routeSessionKey, ""),
		Allocation: session.Allocation{
			RouteScopeKey: routeSessionKey,
			SessionKey:    record.Route.SessionKey,
		},
		SessionKey: record.Route.SessionKey,
	}
	claim, _, claimed := al.claimRuntimeRouteSession(
		target,
		fmt.Sprintf("pending-interaction-cancel-recovery-%s-%d", record.ShortID, al.turnSeq.Add(1)),
	)
	if !claimed {
		return false
	}
	defer claim.releaseIfOwned()
	al.takePendingStop(newRuntimeSessionScope(
		agent.Workspace,
		interactionContinuationSessionKey(record),
	))
	if err := al.ensureInteractionCancellationToolResult(
		ctx,
		agent,
		record,
		record.FailureCode,
	); err != nil {
		return false
	}
	completed, err := registry.CompleteCancellation(record.ID, record.Revision)
	if err != nil {
		return false
	}
	al.cleanupInteractionOriginTools(ctx, agent, completed)
	_ = al.drainDeferredInteractionIngress(
		ctx,
		workspace,
		record.Route,
		inboundContextForInteraction(record.Route),
	)
	return true
}

func (al *AgentLoop) recoverInteractionPrompt(
	ctx context.Context,
	workspace string,
	registry *interactions.Registry,
	record interactions.Record,
) bool {
	return al.recoverInteractionPromptAt(ctx, workspace, registry, record, time.Now().UTC())
}

func (al *AgentLoop) recoverInteractionPromptAt(
	ctx context.Context,
	workspace string,
	registry *interactions.Registry,
	record interactions.Record,
	now time.Time,
) bool {
	if record.PromptDeliveryID != "" {
		if coordinator := al.outboundCoordinator(); coordinator != nil {
			if intent, getErr := coordinator.Get(record.PromptDeliveryID); getErr == nil {
				if handled, recovered := al.settleRecoveredInteractionPrompt(
					ctx, workspace, registry, record, intent, now,
				); handled {
					return recovered
				}
			}
		}
	}
	current, intent, err := al.deliverInteractionPrompt(ctx, registry, workspace, record)
	if err == nil {
		return true
	}
	_, recovered := al.settleRecoveredInteractionPrompt(ctx, workspace, registry, current, intent, now)
	return recovered
}

func (al *AgentLoop) settleRecoveredInteractionPrompt(
	ctx context.Context,
	workspace string,
	registry *interactions.Registry,
	record interactions.Record,
	intent outbox.Intent,
	now time.Time,
) (bool, bool) {
	switch intent.Status {
	case outbox.StatusDelivered:
		_, err := registry.MarkWaiting(record.ID, record.Revision)
		return true, err == nil
	case outbox.StatusAmbiguous:
		claimed, claimErr := registry.ClaimDeliveryUnknown(
			record.ID,
			record.Revision,
			record.PromptDeliveryID,
		)
		return true, claimErr == nil && al.recoverClaimedInteraction(ctx, workspace, claimed)
	case outbox.StatusDefinitelyFailed:
		if intent.RetryExhausted() {
			return true, al.recoverPromptDeliveryExhaustion(ctx, workspace, registry, record)
		}
		if !intent.RetryAfter.IsZero() && now.Before(intent.RetryAfter) {
			return true, false
		}
	}
	return false, false
}

// ReconcileRecoveredInteractionPromptAdmission verifies that a recovered
// interaction prompt is still wanted immediately before gateway publication.
// Stale prompts are durably abandoned so future restarts cannot revive them.
func (al *AgentLoop) ReconcileRecoveredInteractionPromptAdmission(
	admission outbox.Admission,
	now time.Time,
) (bool, error) {
	interactionID, tagged := recoveredInteractionPromptID(admission.Intent)
	if !tagged {
		return true, nil
	}
	registry := al.interactionRegistryForWorkspace(admission.Intent.OwnerWorkspace)
	if registry == nil {
		return false, interactions.ErrStoreUnavailable
	}
	if err := registry.LastLoadError(); err != nil {
		return false, fmt.Errorf("load interaction registry: %w", err)
	}
	record, active := activeInteractionPrompt(registry, interactionID, admission.Intent.ID, now)
	if active && al.interactionAgentAvailable(admission.Intent.OwnerWorkspace, record) {
		return true, nil
	}
	coordinator := al.outboundCoordinator()
	if coordinator == nil {
		return false, fmt.Errorf("outbound coordinator is unavailable")
	}
	if _, err := coordinator.Abandon(admission.Intent.ID, outbox.Outcome{
		Error: "interaction prompt is no longer active",
	}); err != nil {
		return false, fmt.Errorf("abandon inactive interaction prompt: %w", err)
	}
	return false, nil
}

func (al *AgentLoop) validateInteractionPromptPublication(
	registry *interactions.Registry,
	workspace string,
	record interactions.Record,
	now time.Time,
) error {
	if registry == nil {
		return interactions.ErrStoreUnavailable
	}
	if err := registry.LastLoadError(); err != nil {
		return fmt.Errorf("load interaction registry: %w", err)
	}
	current, active := activeInteractionPrompt(registry, record.ID, record.PromptDeliveryID, now)
	if active && al.interactionAgentAvailable(workspace, current) {
		return nil
	}
	coordinator := al.outboundCoordinator()
	if coordinator == nil {
		return fmt.Errorf("outbound coordinator is unavailable")
	}
	if _, err := coordinator.Abandon(record.PromptDeliveryID, outbox.Outcome{
		Error: "interaction prompt is no longer active",
	}); err != nil {
		return fmt.Errorf("abandon inactive interaction prompt: %w", err)
	}
	return fmt.Errorf("interaction prompt %q is no longer active", record.ID)
}

func activeInteractionPrompt(
	registry *interactions.Registry,
	interactionID string,
	deliveryID string,
	now time.Time,
) (interactions.Record, bool) {
	if registry == nil {
		return interactions.Record{}, false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	record, found := registry.Get(interactionID)
	active := found && record.Status == interactions.StatusCreated &&
		record.PromptDeliveryID == deliveryID &&
		(record.ExpiresAt <= 0 || now.UnixMilli() < record.ExpiresAt)
	return record, active
}

// SettleRecoveredInteractionPromptAdmission advances an interaction from the
// exact terminal receipt of a gateway-owned recovered prompt admission.
func (al *AgentLoop) SettleRecoveredInteractionPromptAdmission(
	ctx context.Context,
	admission outbox.Admission,
) error {
	interactionID, tagged := recoveredInteractionPromptID(admission.Intent)
	if !tagged {
		return nil
	}
	coordinator := al.outboundCoordinator()
	if coordinator == nil {
		return fmt.Errorf("outbound coordinator is unavailable")
	}
	intent, err := coordinator.AwaitTerminal(ctx, admission)
	if err != nil {
		return fmt.Errorf("await recovered interaction prompt: %w", err)
	}
	registry := al.interactionRegistryForWorkspace(admission.Intent.OwnerWorkspace)
	if registry == nil {
		return interactions.ErrStoreUnavailable
	}
	if err = registry.LastLoadError(); err != nil {
		return fmt.Errorf("load interaction registry: %w", err)
	}
	record, found := registry.Get(interactionID)
	if !found || record.PromptDeliveryID != admission.Intent.ID {
		return nil
	}
	al.settleRecoveredInteractionPrompt(
		ctx,
		admission.Intent.OwnerWorkspace,
		registry,
		record,
		intent,
		time.Now().UTC(),
	)
	return nil
}

func recoveredInteractionPromptID(intent outbox.Intent) (string, bool) {
	const prefix = "interaction:"
	const suffix = ":prompt"
	sourceID := strings.TrimSpace(intent.Identity.SourceID)
	if intent.Identity.Kind != outbox.KindMessage || intent.Identity.Ordinal != 0 ||
		!strings.HasPrefix(sourceID, prefix) || !strings.HasSuffix(sourceID, suffix) {
		return "", false
	}
	interactionID := strings.TrimSuffix(strings.TrimPrefix(sourceID, prefix), suffix)
	if interactionID == "" || strings.Contains(interactionID, ":") {
		return "", false
	}
	return interactionID, true
}

func (al *AgentLoop) recoverClaimedInteraction(
	ctx context.Context,
	workspace string,
	record interactions.Record,
) bool {
	flightKey, flight, owner := al.startInteractionResumeFlight(workspace, record.ID)
	if !owner {
		return false
	}
	var recoveryErr error
	recoveryHandled := false
	defer func() {
		al.finishInteractionResumeFlight(flightKey, flight, recoveryHandled, recoveryErr)
	}()
	registry := al.interactionRegistryForWorkspace(workspace)
	current, ok := registry.Get(record.ID)
	if !ok {
		recoveryErr = interactions.ErrNotFound
		return false
	}
	record = current
	switch record.Status {
	case interactions.StatusResolved, interactions.StatusCancelled, interactions.StatusFailed:
		recoveryHandled = true
		return false
	case interactions.StatusClaimed:
		al.syncInteractionControls(workspace, record, bus.OutboundInteractionControlsRemove)
	case interactions.StatusResuming:
		al.syncInteractionControls(workspace, record, bus.OutboundInteractionControlsRemove)
		if record.FinalDeliveryState == interactions.DeliveryStateSending ||
			record.FinalDeliveryState == interactions.DeliveryStateAmbiguous {
			if !al.failRecoveredInteraction(
				ctx,
				workspace,
				registry,
				record,
				"final_delivery_ambiguous",
				"final response delivery could not be confirmed and was not retried",
			) {
				return false
			}
			_ = al.drainDeferredInteractionIngress(
				ctx, workspace, record.Route, inboundContextForInteraction(record.Route),
			)
			recoveryHandled = true
			return true
		}
		if !record.FinalDelivered &&
			record.FinalDeliveryTries >= interactions.MaxDeliveryAttempts {
			if !al.failRecoveredInteraction(
				ctx,
				workspace,
				registry,
				record,
				"final_delivery_exhausted",
				"final delivery exhausted its bounded retry budget",
			) {
				return false
			}
			_ = al.drainDeferredInteractionIngress(
				ctx, workspace, record.Route, inboundContextForInteraction(record.Route),
			)
			recoveryHandled = true
			return true
		}
	default:
		return false
	}
	agentRegistry := al.GetRegistry()
	if agentRegistry == nil {
		return false
	}
	agent, ok := agentRegistry.GetAgent(record.Route.AgentID)
	if !ok || agent == nil || (record.Origin.TaskID == "" &&
		strings.TrimSpace(agent.Workspace) != strings.TrimSpace(workspace)) {
		return false
	}
	if err := configureInteractionSteeringHandoff(flight, workspace, record, agent); err != nil {
		recoveryErr = err
		return false
	}
	scope := sessionScopeForRecovery(agent.Sessions, interactionContinuationSessionKey(record))
	if scope == nil {
		scope = &session.SessionScope{
			Version:       1,
			AgentID:       record.Route.AgentID,
			Channel:       record.Route.Channel,
			RouteScopeKey: record.Route.RouteSessionKey,
		}
	}
	routeSessionKey := record.Route.RouteSessionKey
	if routeSessionKey == "" {
		routeSessionKey = record.Route.SessionKey
	}
	target := &inboundDispatchTarget{
		Agent:         agent,
		RouteClaimKey: runtimeRouteClaimKey(routeSessionKey, ""),
		Allocation: session.Allocation{
			RouteScopeKey: routeSessionKey,
			SessionKey:    record.Route.SessionKey,
			Scope:         *session.CloneScope(scope),
		},
		SessionKey: record.Route.SessionKey,
	}
	claim, _, claimed := al.claimRuntimeRouteSession(
		target,
		fmt.Sprintf("pending-interaction-recovery-%s-%d", record.ShortID, al.turnSeq.Add(1)),
	)
	if !claimed {
		return false
	}
	defer claim.releaseIfOwned()
	recoveryHandled = true
	if err := al.resumeClaimedInteractionOwned(
		ctx,
		registry,
		workspace,
		agent,
		scope,
		inboundContextForInteraction(record.Route),
		record,
	); err != nil {
		recoveryErr = err
		logger.WarnCF("agent", "Failed to recover human interaction", map[string]any{
			"interaction_id": record.ID,
			"session_key":    record.Route.SessionKey,
			"error":          err.Error(),
		})
		return false
	}
	if err := al.drainDeferredInteractionIngress(
		ctx,
		workspace,
		record.Route,
		inboundContextForInteraction(record.Route),
	); err != nil {
		logger.WarnCF("agent", "Failed to continue messages after interaction recovery", map[string]any{
			"interaction_id": record.ID,
			"session_key":    record.Route.SessionKey,
			"error":          err.Error(),
		})
	}
	return true
}

func inboundContextForInteraction(route interactions.Route) bus.InboundContext {
	return bus.InboundContext{
		Channel:   route.Channel,
		Account:   route.AccountID,
		ChatID:    route.ChatID,
		ChatType:  route.ChatType,
		TopicID:   route.TopicID,
		SpaceID:   route.SpaceID,
		SpaceType: route.SpaceType,
		SenderID:  route.SenderID,
	}
}
