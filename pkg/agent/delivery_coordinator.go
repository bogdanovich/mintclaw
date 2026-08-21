package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
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
	Context      context.Context
	TurnState    *turnState
	ToolName     string
	CompletionID string
	Result       *toolshared.ToolResult
	Decision     AsyncDeliveryDecision
	TraceScopes  []runtimeevents.TraceScope
}

type asyncToolCompletionDelivery struct {
	currentConfig                   func() *config.Config
	events                          runtimeEventEmitter
	deliverToUser                   func(context.Context, *turnState, *toolshared.ToolResult, string, []runtimeevents.TraceScope) ([]providers.Attachment, toolResultDeliveryOutcome, error)
	synthesizeCompletion            func(context.Context, AsyncCompletionInput) (string, error)
	publishOutbound                 func(context.Context, string, bus.OutboundMessage) error
	asyncTaskDeliveryAlreadyHandled func(workspace, taskID, completionID string) bool
	recordAsyncTaskDeliveryDecision func(workspace string, decision AsyncDeliveryDecision, completionID, sourceTool string)
	updateAsyncTaskDeliveryStatus   func(workspace, taskID string, status taskregistry.DeliveryStatus, completionID, errorSummary string)
}

func (al *AgentLoop) asyncToolCompletionDelivery() *asyncToolCompletionDelivery {
	if al == nil {
		return nil
	}
	return &asyncToolCompletionDelivery{
		currentConfig: al.GetConfig,
		events:        al.runtimeEventEmitter(),
		deliverToUser: al.deliverToolResultToUserWithScopes,
		synthesizeCompletion: func(ctx context.Context, input AsyncCompletionInput) (string, error) {
			return al.processAsyncCompletionWithDelivery(ctx, input, false)
		},
		publishOutbound: func(ctx context.Context, workspace string, msg bus.OutboundMessage) error {
			_, err := al.publishTransactionMessage(ctx, workspace, msg)
			return err
		},
		asyncTaskDeliveryAlreadyHandled: al.asyncTaskDeliveryAlreadyHandled,
		recordAsyncTaskDeliveryDecision: al.recordAsyncTaskDeliveryDecision,
		updateAsyncTaskDeliveryStatus:   al.updateAsyncTaskDeliveryStatus,
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
	deliveryContext := req.Context
	if deliveryContext == nil {
		deliveryContext = context.Background()
	}
	deferDeliverySuccess := outboundTransactionFromContext(deliveryContext) != nil
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
		if content != "" && result.Delivery.Intent != toolshared.DeliverySilent {
			outCtx, outCancel := context.WithTimeout(context.WithoutCancel(deliveryContext), 5*time.Second)
			defer outCancel()
			msg, err := outboundMessageForTraceSettlement(ts, content, req.TraceScopes)
			if err != nil {
				deliveryErr = err.Error()
			} else if err := d.publishMessage(outCtx, ts.workspace, msg); err != nil {
				deliveryErr = err.Error()
			} else {
				delivered = true
			}
		}
		switch {
		case delivered:
			status := taskregistry.DeliveryDelivered
			if deferDeliverySuccess {
				status = taskregistry.DeliveryPending
			}
			d.updateDeliveryStatus(
				ts.workspace,
				delivery.TaskID,
				status,
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
		outCtx, outCancel := context.WithTimeout(context.WithoutCancel(deliveryContext), 5*time.Second)
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
		} else if outcome == toolResultDeliveryNone && strings.TrimSpace(result.ForUser) != "" &&
			result.Delivery.Intent != toolshared.DeliverySilent {
			msg, err := outboundMessageForTraceSettlement(ts, result.ForUser, req.TraceScopes)
			if err != nil {
				userDeliveryErr = err.Error()
			} else if err := d.publishMessage(outCtx, ts.workspace, msg); err != nil {
				userDeliveryErr = err.Error()
			} else {
				userDelivered = true
			}
		} else if outcome == toolResultDeliveryDirect {
			userDelivered = true
		}
		if !delivery.QueueParent {
			if userDelivered {
				status := taskregistry.DeliveryDelivered
				if deferDeliverySuccess {
					status = taskregistry.DeliveryPending
				}
				d.updateDeliveryStatus(
					ts.workspace,
					delivery.TaskID,
					status,
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
	completionCtx, completionCancel := context.WithTimeout(
		context.WithoutCancel(deliveryContext),
		asyncCompletionSynthesisTimeout,
	)
	defer completionCancel()
	synthesized, err := d.synthesizeAsyncCompletion(completionCtx, AsyncCompletionInput{
		SourceTool:   asyncToolName,
		CompletionID: completionID,
		Content:      asyncCompletionPrompt(asyncToolName, content),
		Origin:       origin,
		SenderID:     fmt.Sprintf("async:%s", asyncToolName),
	})
	if err == nil && strings.TrimSpace(synthesized) == "" {
		err = fmt.Errorf("async completion synthesis returned no final response")
	}
	if err == nil {
		var msg bus.OutboundMessage
		msg, err = outboundMessageForTraceSettlement(ts, synthesized, req.TraceScopes)
		if err == nil {
			bus.OutboundMetadata{
				MessageKind:  bus.OutboundMessageKindFinalReply,
				OutboundKind: bus.OutboundKindFinal,
			}.ApplyToContext(&msg.Context)
			err = d.publishMessage(completionCtx, ts.workspace, msg)
		}
	}
	if err != nil {
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
	} else if deferDeliverySuccess {
		d.updateDeliveryStatus(
			ts.workspace,
			delivery.TaskID,
			taskregistry.DeliveryPending,
			completionID,
			"",
		)
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

func (d *asyncToolCompletionDelivery) publishMessage(
	ctx context.Context,
	workspace string,
	msg bus.OutboundMessage,
) error {
	if d == nil || d.publishOutbound == nil {
		return fmt.Errorf("message bus not initialized")
	}
	return d.publishOutbound(ctx, workspace, msg)
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

func (d *asyncToolCompletionDelivery) synthesizeAsyncCompletion(
	ctx context.Context,
	input AsyncCompletionInput,
) (string, error) {
	if d == nil || d.synthesizeCompletion == nil {
		return "", fmt.Errorf("async completion processor is not initialized")
	}
	return d.synthesizeCompletion(ctx, input)
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
	decision.TaskID = result.Control.TaskID
	decision.ContentLen = len(content)
	decision.ForUserLen = len(toolResultUserText(result))
	decision.MediaCount = len(toolResultMediaRefs(result))
	decision.IsError = result.IsError

	if decision.DeliveryMode != toolshared.AsyncDeliveryParentOnly {
		hasUserPayload := decision.ForUserLen > 0 || decision.MediaCount > 0
		decision.PublishToUser = hasUserPayload &&
			(result.Delivery.Intent != toolshared.DeliverySilent ||
				decision.DeliveryMode == toolshared.AsyncDeliveryUserOnly)
	}
	if decision.DeliveryMode != toolshared.AsyncDeliveryUserOnly {
		decision.QueueParent = content != ""
	}
	decision.ParentHandled = !decision.QueueParent && !result.IsError &&
		decision.DeliveryMode == toolshared.AsyncDeliveryUserOnly
	return decision
}

func effectiveAsyncToolResultDelivery(result *toolshared.ToolResult) toolshared.AsyncDeliveryMode {
	if result == nil || result.Delivery.AsyncMode == "" {
		return toolshared.AsyncDeliveryUserAndParent
	}
	return result.Delivery.AsyncMode
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
