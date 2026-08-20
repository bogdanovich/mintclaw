package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/bogdanovich/mintclaw/pkg/agent/interfaces"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// AsyncDeliveryDecision is the routing plan for a completed async tool result.
//
// This is intentionally decision-only for now. The current runtime still
// performs delivery in pipeline_execute.go, but all routing policy should flow
// through this type so media, duplicate, timeout, and restart handling can move
// behind the same coordinator boundary later.
type AsyncDeliveryDecision struct {
	TaskID        string
	DeliveryMode  toolshared.AsyncDeliveryMode
	PublishToUser bool
	QueueParent   bool
	ParentHandled bool
	ContentLen    int
	ForUserLen    int
	MediaCount    int
	IsError       bool
}

type AsyncDeliveryRequest struct {
	TurnState    *turnState
	ToolName     string
	CompletionID string
	Result       *toolshared.ToolResult
	Decision     AsyncDeliveryDecision
	TraceScopes  []runtimeevents.TraceScope
}

type asyncToolCompletionDelivery struct {
	bus                             interfaces.MessageBus
	currentConfig                   func() *config.Config
	events                          runtimeEventEmitter
	deliverToUser                   func(context.Context, *turnState, *toolshared.ToolResult, string, []runtimeevents.TraceScope) ([]providers.Attachment, toolResultDeliveryOutcome, error)
	processCompletion               func(context.Context, AsyncCompletionInput) (string, error)
	asyncTaskDeliveryAlreadyHandled func(workspace, taskID, completionID string) bool
	recordAsyncTaskDeliveryDecision func(workspace string, decision AsyncDeliveryDecision, completionID, sourceTool string)
	updateAsyncTaskDeliveryStatus   func(workspace, taskID string, status taskregistry.DeliveryStatus, completionID, errorSummary string)
	recordCompletionObservation     func(context.Context, *turnState, string, *toolshared.ToolResult) error
	flushPendingObservations        func(context.Context, *turnState) error
}

func (al *AgentLoop) asyncToolCompletionDelivery() *asyncToolCompletionDelivery {
	if al == nil {
		return nil
	}
	return &asyncToolCompletionDelivery{
		bus:                             al.bus,
		currentConfig:                   al.GetConfig,
		events:                          al.runtimeEventEmitter(),
		deliverToUser:                   al.deliverToolResultToUserWithScopes,
		processCompletion:               al.processAsyncCompletion,
		asyncTaskDeliveryAlreadyHandled: al.asyncTaskDeliveryAlreadyHandled,
		recordAsyncTaskDeliveryDecision: al.recordAsyncTaskDeliveryDecision,
		updateAsyncTaskDeliveryStatus:   al.updateAsyncTaskDeliveryStatus,
		recordCompletionObservation:     al.recordAsyncTaskCompletionObservation,
		flushPendingObservations:        al.flushPendingAsyncTaskObservations,
	}
}

func (al *AgentLoop) deliverAsyncToolCompletion(req AsyncDeliveryRequest) {
	al.asyncToolCompletionDelivery().deliverAsyncToolCompletion(req)
}

func (d *asyncToolCompletionDelivery) deliverAsyncToolCompletion(req AsyncDeliveryRequest) {
	ts := req.TurnState
	result := req.Result
	asyncToolName := strings.TrimSpace(req.ToolName)
	if ts == nil || result == nil {
		return
	}
	if asyncToolName == "" {
		asyncToolName = "async_tool"
	}
	delivery := req.Decision
	if delivery.DeliveryMode == "" {
		delivery = decideAsyncToolResultDelivery(result)
	}
	ts = turnStateForAsyncUserDelivery(ts, delivery)
	completionID := strings.TrimSpace(req.CompletionID)
	if err := d.recordTerminalObservation(ts, completionID, result); err != nil {
		logger.WarnCF("agent", "Failed to persist async task completion observation", map[string]any{
			"tool":          asyncToolName,
			"completion_id": completionID,
			"task_id":       delivery.TaskID,
			"error":         err.Error(),
		})
	}
	if d.isAsyncTaskDeliveryAlreadyHandled(ts.workspace, delivery.TaskID, completionID) {
		logger.InfoCF("agent", "Skipping duplicate async delivery",
			map[string]any{
				"tool":          asyncToolName,
				"completion_id": completionID,
				"task_id":       delivery.TaskID,
			})
		return
	}
	d.recordDeliveryDecision(ts.workspace, delivery, completionID, asyncToolName)
	if result.IsError {
		content := strings.TrimSpace(result.ForUser)
		if content == "" {
			content = strings.TrimSpace(result.ContentForLLM())
		}
		delivered := false
		deliveryErr := ""
		if content != "" && !result.Silent {
			outCtx, outCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer outCancel()
			msg, err := outboundMessageForTraceSettlement(ts, content, req.TraceScopes)
			if err != nil {
				deliveryErr = err.Error()
			} else if err := d.publishOutbound(outCtx, msg); err != nil {
				deliveryErr = err.Error()
			} else {
				delivered = true
			}
		}
		switch {
		case delivered:
			d.updateDeliveryStatus(
				ts.workspace,
				delivery.TaskID,
				taskregistry.DeliveryDelivered,
				completionID,
				"",
			)
		case deliveryErr != "":
			d.updateDeliveryStatus(
				ts.workspace,
				delivery.TaskID,
				taskregistry.DeliveryFailed,
				completionID,
				deliveryErr,
			)
		default:
			d.updateDeliveryStatus(
				ts.workspace,
				delivery.TaskID,
				taskregistry.DeliveryNotApplicable,
				completionID,
				"",
			)
		}
		return
	}
	if delivery.PublishToUser {
		outCtx, outCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer outCancel()
		userDelivered := false
		userDeliveryErr := ""
		if _, outcome, err := d.deliverToUserResult(
			outCtx, ts, result, asyncToolName, req.TraceScopes,
		); err != nil {
			userDeliveryErr = err.Error()
			logger.WarnCF("agent", "Failed to deliver async tool result to user",
				map[string]any{
					"tool":    asyncToolName,
					"channel": ts.channel,
					"chat_id": ts.chatID,
					"error":   err.Error(),
				})
		} else if outcome == toolResultDeliveryQueued {
			userDelivered = true
		} else if outcome == toolResultDeliveryNone && strings.TrimSpace(result.ForUser) != "" && !result.Silent {
			msg, err := outboundMessageForTraceSettlement(ts, result.ForUser, req.TraceScopes)
			if err != nil {
				userDeliveryErr = err.Error()
			} else if err := d.publishOutbound(outCtx, msg); err != nil {
				userDeliveryErr = err.Error()
			} else {
				userDelivered = true
			}
		} else if outcome == toolResultDeliveryDirect {
			userDelivered = true
		}
		if !delivery.QueueParent {
			if userDelivered {
				d.updateDeliveryStatus(
					ts.workspace,
					delivery.TaskID,
					taskregistry.DeliveryDelivered,
					completionID,
					"",
				)
			} else if userDeliveryErr != "" {
				d.updateDeliveryStatus(
					ts.workspace,
					delivery.TaskID,
					taskregistry.DeliveryFailed,
					completionID,
					userDeliveryErr,
				)
			} else {
				d.updateDeliveryStatus(
					ts.workspace,
					delivery.TaskID,
					taskregistry.DeliveryNotApplicable,
					completionID,
					"",
				)
			}
			return
		}
	}

	if !delivery.QueueParent {
		d.updateDeliveryStatus(
			ts.workspace,
			delivery.TaskID,
			taskregistry.DeliveryNotApplicable,
			completionID,
			"",
		)
		return
	}

	content := result.ContentForLLM()
	if cfg := d.getConfig(); cfg != nil {
		content = cfg.FilterSensitiveData(content)
	}

	logger.InfoCF("agent", "Async tool completed, publishing result",
		map[string]any{
			"tool":        asyncToolName,
			"content_len": len(content),
			"channel":     ts.channel,
		})
	d.emitEvent(
		runtimeevents.KindAgentFollowUpQueued,
		ts.scope.meta(0, "delivery_coordinator", "turn.follow_up.queued"),
		FollowUpQueuedPayload{
			SourceTool: asyncToolName,
			ContentLen: len(content),
		},
	)
	origin := bus.InboundContext{
		Channel:  ts.channel,
		ChatID:   ts.chatID,
		ChatType: "direct",
		SenderID: fmt.Sprintf("async:%s", asyncToolName),
		TopicID:  originTopicID(ts.opts.Dispatch.InboundContext),
	}
	if ts.opts.Dispatch.InboundContext != nil {
		origin = *cloneInboundContext(ts.opts.Dispatch.InboundContext)
		if strings.TrimSpace(origin.Channel) == "" {
			origin.Channel = ts.channel
		}
		if strings.TrimSpace(origin.ChatID) == "" {
			origin.ChatID = ts.chatID
		}
		if strings.TrimSpace(origin.ChatType) == "" {
			origin.ChatType = "direct"
		}
		origin.SenderID = fmt.Sprintf("async:%s", asyncToolName)
	}
	completionCtx, completionCancel := context.WithTimeout(context.Background(), asyncCompletionSynthesisTimeout)
	defer completionCancel()
	if _, err := d.processAsyncCompletion(completionCtx, AsyncCompletionInput{
		SourceTool:   asyncToolName,
		CompletionID: completionID,
		Content:      asyncCompletionPrompt(asyncToolName, content),
		Origin:       origin,
		SenderID:     fmt.Sprintf("async:%s", asyncToolName),
	}); err != nil {
		d.updateDeliveryStatus(
			ts.workspace,
			delivery.TaskID,
			taskregistry.DeliveryFailed,
			completionID,
			err.Error(),
		)
		logger.WarnCF("agent", "Failed to process async completion",
			map[string]any{
				"tool":          asyncToolName,
				"completion_id": completionID,
				"channel":       ts.channel,
				"chat_id":       ts.chatID,
				"error":         err.Error(),
			})
	} else if delivery.DeliveryMode == toolshared.AsyncDeliveryParentOnly {
		d.updateDeliveryStatus(
			ts.workspace,
			delivery.TaskID,
			taskregistry.DeliverySessionQueued,
			completionID,
			"",
		)
	} else {
		d.updateDeliveryStatus(
			ts.workspace,
			delivery.TaskID,
			taskregistry.DeliveryDelivered,
			completionID,
			"",
		)
	}
}

const asyncTaskObservationResultLimit = 2000

var errAsyncTaskObservationToolBlockOpen = errors.New("async task observation would split an open tool-result block")

func (al *AgentLoop) recordAsyncTaskCompletionObservation(
	ctx context.Context,
	ts *turnState,
	completionID string,
	result *toolshared.ToolResult,
) error {
	if ts == nil || ts.opts.NoHistory || result == nil {
		return nil
	}
	taskID := strings.TrimSpace(result.AsyncTaskID)
	if taskID == "" {
		return nil
	}
	marker := asyncTaskObservationMarker(taskID, completionID)
	state := asyncTaskObjectiveState(result)
	content := strings.TrimSpace(result.ContentForLLM())
	if content == "" {
		content = strings.TrimSpace(toolResultUserText(result))
	}
	if cfg := al.GetConfig(); cfg != nil {
		content = cfg.FilterSensitiveData(content)
	}
	observation := fmt.Sprintf(
		"%s\ntask_id: %s\nstate: %s\nThis task is no longer running.\nResult:\n%s",
		marker,
		sanitizeAsyncTaskIdentifier(taskID, 128),
		state,
		content,
	)
	observation = truncateAsyncTaskObservation(observation, asyncTaskObservationResultLimit)
	registry := al.taskRegistryForWorkspace(ts.workspace)
	if registry != nil {
		record, ok := registry.Get(taskID)
		if ok {
			if !record.HistoryPolicyKnown || record.HistoryDisabled {
				return nil
			}
			if err := registry.Update(taskID, func(stored *taskregistry.Record) {
				stored.PendingObservation = observation
				stored.ObservationMarker = marker
			}); err != nil {
				return fmt.Errorf("persist pending async task observation: %w", err)
			}
			return al.flushPendingAsyncTaskObservation(ctx, ts, taskID)
		}
	}
	ownerAgent, sessionKey, historyDisabled, err := al.asyncTaskObservationTarget(ts, taskID)
	if err != nil {
		return err
	}
	if historyDisabled {
		return nil
	}
	if ownerAgent == nil || ownerAgent.Sessions == nil {
		return fmt.Errorf("async task %q requester session store is unavailable", taskID)
	}
	if sessionKey == "" {
		sessionKey = session.BuildMainSessionKey(ownerAgent.ID)
	}
	_, err = ownerAgent.Sessions.MutateTurnHistory(ctx, sessionKey, func(history []providers.Message) (
		[]providers.Message,
		bool,
		error,
	) {
		return appendAsyncTaskCompletionObservation(history, marker, observation)
	})
	return err
}

func (al *AgentLoop) flushPendingAsyncTaskObservations(ctx context.Context, ts *turnState) error {
	if ts == nil || ts.opts.NoHistory || ts.agent == nil {
		return nil
	}
	registry := al.taskRegistryForWorkspace(ts.workspace)
	if registry == nil {
		return nil
	}
	var flushErrors []error
	for _, record := range registry.List() {
		if strings.TrimSpace(record.PendingObservation) == "" || record.OwnerKey != ts.agent.ID ||
			record.RequesterSessionKey != ts.sessionKey {
			continue
		}
		if err := al.flushPendingAsyncTaskObservation(ctx, ts, record.TaskID); err != nil {
			flushErrors = append(flushErrors, err)
		}
	}
	return errors.Join(flushErrors...)
}

func (al *AgentLoop) flushPendingAsyncTaskObservation(ctx context.Context, ts *turnState, taskID string) error {
	registry := al.taskRegistryForWorkspace(ts.workspace)
	if registry == nil {
		return nil
	}
	record, ok := registry.Get(taskID)
	if !ok || strings.TrimSpace(record.PendingObservation) == "" {
		return nil
	}
	marker := record.ObservationMarker
	observation := record.PendingObservation
	ownerAgent, sessionKey, historyDisabled, err := al.asyncTaskObservationTarget(ts, taskID)
	if err != nil {
		return err
	}
	if historyDisabled {
		return registry.Update(taskID, clearPendingAsyncTaskObservation)
	}
	if ownerAgent == nil || ownerAgent.Sessions == nil {
		return fmt.Errorf("async task %q requester session store is unavailable", taskID)
	}
	if sessionKey == "" {
		sessionKey = session.BuildMainSessionKey(ownerAgent.ID)
	}
	_, err = ownerAgent.Sessions.MutateTurnHistory(ctx, sessionKey, func(history []providers.Message) (
		[]providers.Message,
		bool,
		error,
	) {
		return appendAsyncTaskCompletionObservation(history, marker, observation)
	})
	if errors.Is(err, errAsyncTaskObservationToolBlockOpen) {
		return nil
	}
	if err != nil {
		return err
	}
	return registry.Update(taskID, func(stored *taskregistry.Record) {
		if stored.ObservationMarker == marker {
			clearPendingAsyncTaskObservation(stored)
		}
	})
}

func clearPendingAsyncTaskObservation(record *taskregistry.Record) {
	record.PendingObservation = ""
	record.ObservationMarker = ""
}

func appendAsyncTaskCompletionObservation(
	history []providers.Message,
	marker string,
	observation string,
) ([]providers.Message, bool, error) {
	for _, message := range history {
		if message.Role == "assistant" && strings.HasPrefix(message.Content, marker) {
			return history, false, nil
		}
	}
	if trailingToolResultBlockIncomplete(history) {
		return history, false, errAsyncTaskObservationToolBlockOpen
	}
	return append(history, providers.Message{Role: "assistant", Content: observation}), true, nil
}

func trailingToolResultBlockIncomplete(history []providers.Message) bool {
	index := len(history) - 1
	for index >= 0 && history[index].Role == "tool" {
		index--
	}
	if index < 0 || history[index].Role != "assistant" || len(history[index].ToolCalls) == 0 {
		return false
	}
	expected := make(map[string]struct{}, len(history[index].ToolCalls))
	for _, call := range history[index].ToolCalls {
		callID := strings.TrimSpace(call.ID)
		if callID == "" {
			return true
		}
		expected[callID] = struct{}{}
	}
	for _, message := range history[index+1:] {
		if message.Role == "tool" {
			delete(expected, strings.TrimSpace(message.ToolCallID))
		}
	}
	return len(expected) > 0
}

func (al *AgentLoop) asyncTaskObservationTarget(
	ts *turnState,
	taskID string,
) (*AgentInstance, string, bool, error) {
	if ts == nil {
		return nil, "", false, nil
	}
	ownerAgent := ts.agent
	sessionKey := strings.TrimSpace(ts.sessionKey)
	registry := al.taskRegistryForWorkspace(ts.workspace)
	if registry == nil {
		return ownerAgent, sessionKey, false, nil
	}
	record, ok := registry.Get(taskID)
	if !ok {
		return ownerAgent, sessionKey, false, nil
	}
	// Records created before requester-history provenance was persisted cannot
	// safely inherit the reconstructed turn's policy after an upgrade. Treat
	// them as history-disabled instead of guessing that an absent bool means
	// history was enabled.
	if !record.HistoryPolicyKnown {
		return nil, sessionKey, true, nil
	}
	if requesterSessionKey := strings.TrimSpace(record.RequesterSessionKey); requesterSessionKey != "" {
		sessionKey = requesterSessionKey
	}
	ownerKey := strings.TrimSpace(record.OwnerKey)
	if ownerKey == "" && strings.TrimSpace(record.RequesterSessionKey) != "" {
		return nil, sessionKey, record.HistoryDisabled, fmt.Errorf(
			"async task %q requester owner is missing", taskID,
		)
	}
	if ownerKey != "" {
		agent, found := al.GetRegistry().GetAgent(ownerKey)
		if !found || agent == nil {
			return nil, sessionKey, record.HistoryDisabled, fmt.Errorf(
				"async task %q requester owner %q is unavailable", taskID, ownerKey,
			)
		}
		ownerAgent = agent
	}
	return ownerAgent, sessionKey, record.HistoryDisabled, nil
}

func asyncTaskObservationMarker(taskID, completionID string) string {
	digest := sha256.Sum256([]byte(taskID + "\x00" + completionID))
	return fmt.Sprintf("[Background task completion: %x]", digest[:16])
}

func sanitizeAsyncTaskIdentifier(value string, limit int) string {
	value = strings.Map(func(char rune) rune {
		if unicode.IsControl(char) {
			return ' '
		}
		return char
	}, strings.TrimSpace(value))
	value = strings.Join(strings.Fields(value), " ")
	return truncateAsyncTaskObservation(value, limit)
}

func asyncTaskObjectiveState(result *toolshared.ToolResult) string {
	if result == nil {
		return "failed"
	}
	if result.IsError {
		return "failed"
	}
	if result.Deliverable != nil && result.Deliverable.ObjectiveOutcome != nil {
		return string(result.Deliverable.ObjectiveOutcome.Status)
	}
	if result.Completion != nil && result.Completion.ObjectiveOutcome != nil {
		return string(result.Completion.ObjectiveOutcome.Status)
	}
	return "succeeded"
}

func truncateAsyncTaskObservation(value string, limit int) string {
	chars := []rune(value)
	if limit <= 0 || len(chars) <= limit {
		return value
	}
	if limit == 1 {
		return "…"
	}
	return string(chars[:limit-1]) + "…"
}

func (d *asyncToolCompletionDelivery) recordTerminalObservation(
	ts *turnState,
	completionID string,
	result *toolshared.ToolResult,
) error {
	if d == nil || d.recordCompletionObservation == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return d.recordCompletionObservation(ctx, ts, completionID, result)
}

func (d *asyncToolCompletionDelivery) flushPendingAsyncTaskObservations(
	ctx context.Context,
	ts *turnState,
) error {
	if d == nil || d.flushPendingObservations == nil {
		return nil
	}
	return d.flushPendingObservations(ctx, ts)
}

func turnStateForAsyncUserDelivery(
	ts *turnState,
	delivery AsyncDeliveryDecision,
) *turnState {
	if ts == nil || delivery.DeliveryMode != toolshared.AsyncDeliveryUserOnly {
		return ts
	}

	cloned := &turnState{
		agent:      ts.agent,
		opts:       ts.opts,
		turnID:     ts.turnID,
		agentID:    ts.agentID,
		sessionKey: ts.sessionKey,
		channel:    ts.channel,
		chatID:     ts.chatID,
		workspace:  ts.workspace,
	}
	inbound := cloneInboundContext(ts.opts.Dispatch.InboundContext)
	if inbound == nil {
		inbound = &bus.InboundContext{}
	}
	bus.OutboundMetadata{
		MessageKind:  bus.OutboundMessageKindFinalReply,
		OutboundKind: bus.OutboundKindFinal,
	}.ApplyToContext(inbound)
	cloned.opts.Dispatch.InboundContext = inbound
	return cloned
}

func (d *asyncToolCompletionDelivery) publishOutbound(ctx context.Context, msg bus.OutboundMessage) error {
	if d == nil || d.bus == nil {
		return fmt.Errorf("message bus not initialized")
	}
	return d.bus.PublishOutbound(ctx, msg)
}

func (d *asyncToolCompletionDelivery) getConfig() *config.Config {
	if d == nil || d.currentConfig == nil {
		return nil
	}
	return d.currentConfig()
}

func (d *asyncToolCompletionDelivery) deliverToUserResult(
	ctx context.Context,
	ts *turnState,
	result *toolshared.ToolResult,
	toolName string,
	traceScopes []runtimeevents.TraceScope,
) ([]providers.Attachment, toolResultDeliveryOutcome, error) {
	if d == nil || d.deliverToUser == nil {
		return nil, toolResultDeliveryNone, fmt.Errorf("tool result delivery is not initialized")
	}
	return d.deliverToUser(ctx, ts, result, toolName, traceScopes)
}

func outboundMessageForTraceSettlement(
	ts *turnState,
	content string,
	traceScopes []runtimeevents.TraceScope,
) (bus.OutboundMessage, error) {
	msg := outboundMessageForTurn(ts, content)
	if err := bus.SetOutboundTraceScopes(&msg, traceScopes); err != nil {
		return bus.OutboundMessage{}, err
	}
	msg.TraceSettlement = len(msg.TraceScopes) > 0
	return msg, nil
}

func (d *asyncToolCompletionDelivery) processAsyncCompletion(
	ctx context.Context,
	input AsyncCompletionInput,
) (string, error) {
	if d == nil || d.processCompletion == nil {
		return "", fmt.Errorf("async completion processor is not initialized")
	}
	return d.processCompletion(ctx, input)
}

func (d *asyncToolCompletionDelivery) isAsyncTaskDeliveryAlreadyHandled(
	workspace,
	taskID,
	completionID string,
) bool {
	if d == nil || d.asyncTaskDeliveryAlreadyHandled == nil {
		return false
	}
	return d.asyncTaskDeliveryAlreadyHandled(workspace, taskID, completionID)
}

func (d *asyncToolCompletionDelivery) recordDeliveryDecision(
	workspace string,
	decision AsyncDeliveryDecision,
	completionID,
	sourceTool string,
) {
	if d == nil || d.recordAsyncTaskDeliveryDecision == nil {
		return
	}
	d.recordAsyncTaskDeliveryDecision(workspace, decision, completionID, sourceTool)
}

func (d *asyncToolCompletionDelivery) updateDeliveryStatus(
	workspace,
	taskID string,
	status taskregistry.DeliveryStatus,
	completionID,
	errorSummary string,
) {
	if d == nil || d.updateAsyncTaskDeliveryStatus == nil {
		return
	}
	d.updateAsyncTaskDeliveryStatus(workspace, taskID, status, completionID, errorSummary)
}

func (d *asyncToolCompletionDelivery) emitEvent(kind runtimeevents.Kind, meta HookMeta, payload any) {
	if d == nil || d.events == nil {
		return
	}
	d.events.emitEvent(kind, meta, payload)
}

func decideAsyncToolResultDelivery(result *toolshared.ToolResult) AsyncDeliveryDecision {
	decision := AsyncDeliveryDecision{
		DeliveryMode: effectiveAsyncToolResultDelivery(result),
	}
	if result == nil {
		return decision
	}

	content := result.ContentForLLM()
	decision.TaskID = result.AsyncTaskID
	decision.ContentLen = len(content)
	decision.ForUserLen = len(toolResultUserText(result))
	decision.MediaCount = len(result.Media)
	if result.Completion != nil {
		decision.MediaCount += len(result.Completion.Media)
	}
	decision.IsError = result.IsError

	if decision.DeliveryMode != toolshared.AsyncDeliveryParentOnly {
		hasUserPayload := decision.ForUserLen > 0 || decision.MediaCount > 0
		decision.PublishToUser = hasUserPayload &&
			(!result.Silent || decision.DeliveryMode == toolshared.AsyncDeliveryUserOnly)
	}
	if decision.DeliveryMode != toolshared.AsyncDeliveryUserOnly {
		decision.QueueParent = content != ""
	}
	decision.ParentHandled = !decision.QueueParent && !result.IsError &&
		decision.DeliveryMode == toolshared.AsyncDeliveryUserOnly
	return decision
}

func effectiveAsyncToolResultDelivery(result *toolshared.ToolResult) toolshared.AsyncDeliveryMode {
	if result == nil || result.AsyncDelivery == "" {
		return toolshared.AsyncDeliveryUserAndParent
	}
	return result.AsyncDelivery
}

func asyncDeliveryModeFromToolArgs(toolName string, args map[string]any) (toolshared.AsyncDeliveryMode, error) {
	if toolName != "spawn" && toolName != "delegate" {
		return toolshared.AsyncDeliveryUserAndParent, nil
	}
	raw, ok := args["delivery_mode"]
	if !ok || raw == nil {
		if toolName == "spawn" {
			return toolshared.AsyncDeliveryUserOnly, nil
		}
		return toolshared.AsyncDeliveryParentOnly, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", nil
	}
	switch mode := toolshared.AsyncDeliveryMode(strings.TrimSpace(value)); mode {
	case toolshared.AsyncDeliveryUserOnly, toolshared.AsyncDeliveryParentOnly, toolshared.AsyncDeliveryUserAndParent:
		return mode, nil
	default:
		return "", nil
	}
}
