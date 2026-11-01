package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/commands"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// handleDocumentIntakeInbound uses existing session ownership and steering.
// Neither path ever receives the raw utterance after protected admission.
func (c *inboundTurnCoordinator) handleDocumentIntakeInbound(
	ctx context.Context, msg bus.InboundMessage, target *inboundDispatchTarget,
) bool {
	guarded := !msg.Context.DocumentIntake.IsZero()
	if target == nil || target.Agent == nil {
		return guarded
	}
	tool, available := target.Agent.Tools.GetRegistered("document")
	documentTool, supported := tool.(*tools.DocumentTool)
	if !available || !supported || !documentTool.ProtectedFormIntakeConfigured() {
		if !guarded {
			return false
		}
		c.rejectDocumentIntakeInbound(
			ctx,
			msg,
			target,
			"Protected form intake is unavailable. The message was not sent to ordinary chat.",
		)
		return true
	}
	toolCtx := toolshared.WithToolContext(ctx, msg.Context.Channel, msg.Context.ChatID)
	toolCtx = toolshared.WithToolTopicID(toolCtx, msg.Context.TopicID)
	toolCtx = toolshared.WithToolInboundMetadata(toolCtx, msg.Context)
	toolCtx = toolshared.WithToolSessionContext(toolCtx, target.Agent.ID, target.SessionKey, &target.Allocation.Scope)
	toolCtx = toolshared.WithToolRouteSessionKey(
		toolCtx,
		firstNonEmptyString(target.Allocation.RouteScopeKey, target.SessionKey),
	)
	toolCtx = toolshared.WithToolExecutionIdentity(toolCtx, target.Agent.Workspace, "form-ingress")
	progress, err := documentTool.FindProtectedFormIntake(toolCtx)
	if guarded && (strings.HasPrefix(msg.Context.DocumentIntake.Reference, "form_route.") ||
		strings.HasPrefix(msg.Context.DocumentIntake.Reference, "form_route_answer.")) {
		progress, err = documentTool.BoundProtectedFormIntake(toolCtx, msg.Context.DocumentIntake)
	}
	if err != nil {
		c.rejectDocumentIntakeInbound(
			ctx,
			msg,
			target,
			"Protected form state could not be read. Nothing was sent to ordinary chat; please retry.",
		)
		return true
	}
	if progress == nil {
		if !guarded {
			return false
		}
		c.rejectDocumentIntakeInbound(
			ctx,
			msg,
			target,
			"This protected form conversation is no longer active. Its message was not replayed into ordinary chat.",
		)
		return true
	}
	guard := msg.Context.DocumentIntake
	if msg.SpoolID == "" {
		c.rejectDocumentIntakeInbound(
			ctx,
			msg,
			target,
			"Protected form input requires durable transport. Nothing was applied or sent to ordinary chat.",
		)
		return true
	}
	if guarded && (!guard.Valid() || guard.JobID != progress.JobID || guard.ModeID != progress.ModeID) {
		c.rejectDocumentIntakeInbound(
			ctx,
			msg,
			target,
			"The protected form binding does not match. The message was not applied or sent to ordinary chat.",
		)
		return true
	}
	if c.routeDocumentTaskScopeAnswer(ctx, msg, target) {
		return true
	}
	if !guarded {
		guard = bus.InboundDocumentIntakeGuard{JobID: progress.JobID, ModeID: progress.ModeID}
		msg.Context.DocumentIntake = guard
		if err = c.al.turns.inbound.persistContext(ctx, msg); err != nil {
			c.al.turns.inbound.release(ctx, msg, err)
			return true
		}
	}
	control := strings.TrimSpace(msg.Content)
	if control == "/pause_form" || control == "/resume_form" || control == "/cancel_form" {
		action := strings.TrimSuffix(strings.TrimPrefix(control, "/"), "_form")
		if err = documentTool.ControlProtectedFormIntake(toolCtx, guard, action); err != nil {
			c.rejectDocumentIntakeInbound(
				ctx,
				msg,
				target,
				"The form control could not be fully confirmed. No output was approved; check the same form's status before retrying.",
			)
		} else {
			c.rejectDocumentIntakeInbound(
				ctx,
				msg,
				target,
				"Form control applied: "+action+". No output was approved or submitted.",
			)
		}
		return true
	}
	routeTask := documentTool.ProtectedFormTaskRoutingConfigured() &&
		!strings.HasPrefix(guard.Reference, "form_intake.")
	if (!routeTask && len(msg.Media) != 0) || (strings.TrimSpace(msg.Content) == "" && len(msg.Media) == 0) {
		c.rejectDocumentIntakeInbound(
			ctx,
			msg,
			target,
			"Please send form details as text. The current form and accepted data are preserved.",
		)
		return true
	}
	var reference string
	if control == "/retry_form" {
		reference, err = documentTool.RetryProtectedFormIntake(toolCtx, guard)
	} else {
		var identity string
		identity, err = protectedAnswerIdempotencyKey("", msg)
		if err != nil {
			c.rejectDocumentIntakeInbound(
				ctx,
				msg,
				target,
				"This message has no durable identity. Form details were not applied; please resend them.",
			)
			return true
		}
		if routeTask {
			reference, err = documentTool.AdmitProtectedFormTask(toolCtx, guard, identity, msg.Content, msg.Media)
		} else {
			reference, err = documentTool.AdmitProtectedFormIntake(toolCtx, guard, identity, msg.Content)
		}
	}
	if err != nil || guard.Reference != "" && guard.Reference != reference {
		c.rejectDocumentIntakeInbound(
			ctx,
			msg,
			target,
			"Form details could not be admitted or retried. Accepted data are preserved; use /retry_form for retained input, /resume_form if paused, /pause_form or /cancel_form.",
		)
		return true
	}
	msg.Context.DocumentIntake.Reference = reference
	if err = c.al.turns.inbound.persistContext(ctx, msg); err != nil {
		c.al.turns.inbound.release(ctx, msg, err)
		return true
	}
	msg.Content = "Protected form input is ready. Continue the same document using its opaque receipt; do not ask the user to repeat admitted data."
	msg.Media = nil
	msg.Context.Interaction = bus.InboundInteractionProjection{}
	claim, activeTarget, claimed := c.claimSession(targetWithInboundRelationRoot(target, msg))
	if !claimed {
		c.handleBusySession(ctx, msg, activeTarget)
		return true
	}
	c.startWorker(ctx, msg, activeTarget, claim)
	return true
}

// An unanswered task-scope question owns the next bare reply, not a new root
// task. Freeze its exact interaction identity in the existing durable ingress
// before the ordinary answer handler can claim it. Replays cannot bind to a
// later question or release the answer as a standalone task.
func (c *inboundTurnCoordinator) routeDocumentTaskScopeAnswer(
	ctx context.Context, msg bus.InboundMessage, target *inboundDispatchTarget,
) bool {
	if msg.Context.DocumentIntake.Reference != "" || len(msg.Media) != 0 ||
		!msg.Context.Interaction.IsZero() || strings.TrimSpace(msg.Content) == "" ||
		commands.HasCommandPrefix(msg.Content) {
		return false
	}
	registry := c.al.interactionRegistryForWorkspace(target.Agent.Workspace)
	if registry == nil {
		return false
	}
	if err := registry.LastLoadError(); err != nil {
		c.al.turns.inbound.release(ctx, msg, errors.New("protected task question state is unavailable"))
		return true
	}
	record, ok := activeInteractionForSession(registry, target.SessionKey)
	if !ok || record.Kind != interactions.KindQuestion ||
		(record.Status != interactions.StatusWaiting && record.Status != interactions.StatusCreated) ||
		!interactionRouteAuthorizes(record.Route, target, msg.Context) ||
		!document.IsFormTaskRouteQuestionBinding(record.ProtectedAnswer) {
		return false
	}
	msg.Context.DocumentIntake = bus.InboundDocumentIntakeGuard{}
	msg.Context.Interaction = bus.InboundInteractionProjection{ShortID: record.ShortID, Response: msg.Content}
	if err := c.al.turns.inbound.persistContext(ctx, msg); err != nil {
		c.al.turns.inbound.release(ctx, msg, err)
		return true
	}
	return c.routeProjectedInteractionAnswer(ctx, msg, target)
}

func (c *inboundTurnCoordinator) rejectDocumentIntakeInbound(
	ctx context.Context, msg bus.InboundMessage, target *inboundDispatchTarget, notice string,
) {
	admission := c.al.publishInteractionNoticeAdmission(ctx, msg, target.SessionKey, notice)
	_ = c.al.settleInboundAdmission(ctx, msg, admission)
}
