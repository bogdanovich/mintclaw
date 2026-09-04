package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/commands"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/session"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
)

// interactionAuthorizationContext is the resolved route identity presented to
// the application service. Ingress remains responsible for resolving it; the
// service rechecks it against the durable record before changing state.
type interactionAuthorizationContext struct {
	SessionKey    string
	RouteScopeKey string
	Inbound       bus.InboundContext
}

func (authorization interactionAuthorizationContext) authorizes(route interactions.Route) bool {
	if route.SessionKey != authorization.SessionKey ||
		route.Channel != authorization.Inbound.Channel ||
		route.ChatID != authorization.Inbound.ChatID ||
		route.SenderID != authorization.Inbound.SenderID {
		return false
	}
	if route.RouteSessionKey != "" && route.RouteSessionKey != authorization.RouteScopeKey {
		return false
	}
	checks := [][2]string{
		{route.AccountID, authorization.Inbound.Account},
		{route.ChatType, authorization.Inbound.ChatType},
		{route.TopicID, authorization.Inbound.TopicID},
		{route.SpaceID, authorization.Inbound.SpaceID},
		{route.SpaceType, authorization.Inbound.SpaceType},
	}
	for _, check := range checks {
		if check[0] != "" && check[0] != check[1] {
			return false
		}
	}
	return true
}

// answerInteractionCommand is the complete input required to accept and
// resume one durable interaction. It deliberately contains resolved values,
// not an inboundDispatchTarget owned by the transport coordinator.
type answerInteractionCommand struct {
	Message       bus.InboundMessage
	Authorization interactionAuthorizationContext
	Workspace     string
	Agent         *AgentInstance
	Scope         *session.SessionScope
}

// cancelInteractionCommand separates command recognition at ingress from the
// ordered cancellation transaction owned by interactionService.
type cancelInteractionCommand struct {
	Message       bus.InboundMessage
	Authorization interactionAuthorizationContext
	Workspace     string
	Agent         *AgentInstance
	Target        *inboundDispatchTarget
	ControlName   string
	FailureCode   string
}

type interactionCancellationEffects struct {
	CancellationFenced         bool
	ContinuationAbortRequested bool
	ControlsRemovalRequested   bool
	ToolResultPersisted        bool
	TaskCancelled              bool
	CancellationCompleted      bool
	OriginCleanupRequested     bool
}

type interactionControlCancellationResult struct {
	Matched        bool
	Canceled       bool
	Failed         bool
	CommandHandled bool
	TaskID         string
	Kind           interactions.Kind
	Effects        interactionCancellationEffects
}

type interactionAnswerEffects struct {
	AnswerPersisted          bool
	ControlsRemovalRequested bool
	ContinuationQueued       bool
	ResumeAttempted          bool
}

type answerInteractionResult struct {
	Ownership interactionInboundOwnership
	Admission finalResponseAdmission
	Record    interactions.Record
	Effects   interactionAnswerEffects
}

// interactionService owns application ordering around the durable interaction
// state machine. Registry remains the domain owner; AgentLoop supplies the
// concrete runtime effects needed to continue an accepted answer.
type interactionService struct {
	runtime *AgentLoop
}

func newInteractionService(runtime *AgentLoop) interactionService {
	return interactionService{runtime: runtime}
}

func newAnswerInteractionCommand(
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) (answerInteractionCommand, error) {
	if target == nil || target.Agent == nil {
		return answerInteractionCommand{}, fmt.Errorf("interaction route is unavailable")
	}
	return answerInteractionCommand{
		Message: msg,
		Authorization: interactionAuthorizationContext{
			SessionKey:    target.SessionKey,
			RouteScopeKey: target.Allocation.RouteScopeKey,
			Inbound:       msg.Context,
		},
		Workspace: target.Agent.Workspace,
		Agent:     target.Agent,
		Scope:     session.CloneScope(&target.Allocation.Scope),
	}, nil
}

func newCancelInteractionCommand(
	msg bus.InboundMessage,
	target *inboundDispatchTarget,
) (cancelInteractionCommand, bool) {
	if strings.TrimSpace(msg.Context.Raw[bus.InboundMetadataKeyInteractionResponse]) != "" ||
		target == nil || target.Agent == nil {
		return cancelInteractionCommand{}, false
	}
	name, matched := commands.CommandName(msg.Content)
	if strings.TrimSpace(msg.Context.Raw[bus.InboundMetadataKeyInteractionChoice]) ==
		bus.InboundInteractionChoiceCancel {
		name = "stop"
		matched = true
	}
	if !matched || (name != "new" && name != "reset" && name != "clear" && name != "stop") {
		return cancelInteractionCommand{}, false
	}
	return cancelInteractionCommand{
		Message: msg,
		Authorization: interactionAuthorizationContext{
			SessionKey:    target.SessionKey,
			RouteScopeKey: target.Allocation.RouteScopeKey,
			Inbound:       msg.Context,
		},
		Workspace:   target.Agent.Workspace,
		Agent:       target.Agent,
		Target:      target,
		ControlName: name,
		FailureCode: "session_control_" + name,
	}, true
}

func (service interactionService) Answer(
	ctx context.Context,
	command answerInteractionCommand,
) (answerInteractionResult, error) {
	notRequired := finalResponseAdmission{status: finalResponseAdmissionNotRequired}
	result := answerInteractionResult{
		Ownership: interactionInboundCallerOwned,
		Admission: notRequired,
	}
	if service.runtime == nil || command.Agent == nil || strings.TrimSpace(command.Workspace) == "" {
		return result, fmt.Errorf("interaction answer runtime is unavailable")
	}

	registry := service.runtime.interactionRegistryForWorkspace(command.Workspace)
	if registry.LastLoadError() != nil {
		return service.notice(
			ctx,
			command,
			result,
			"Pending input state is unavailable; this session cannot continue until it is recovered.",
		)
	}
	record, ok := activeInteractionForSession(registry, command.Authorization.SessionKey)
	if !ok {
		return result, fmt.Errorf(
			"active interaction disappeared for session %q",
			command.Authorization.SessionKey,
		)
	}
	result.Record = record
	if record.Status == interactions.StatusClaimed || record.Status == interactions.StatusResuming {
		if interactionInboundReplaysAnswer(record, command.Message.Context) {
			result.Ownership = interactionInboundClaimed
			if err := service.runtime.settleInboundAdmission(ctx, command.Message, notRequired); err != nil {
				return result, err
			}
			result.Effects.ResumeAttempted = true
			return result, service.resume(ctx, registry, command, record)
		}
		if _, _, explicit := splitExplicitInteractionAnswer(command.Message.Content); explicit {
			logExplicitInteractionAnswerDisposition(
				record,
				command.Message,
				explicitInteractionAnswerDuplicate,
			)
			return service.notice(
				ctx,
				command,
				result,
				"An answer has already been accepted for this interaction.",
			)
		}
		continuationAgent := service.runtime.interactionContinuationAgent(record, command.Agent)
		if continuationAgent == nil {
			return result, fmt.Errorf("interaction continuation agent is unavailable")
		}
		if err := service.runtime.enqueueInteractionContinuationInboundForScope(
			ctx,
			command.Message,
			newRuntimeSessionScope(
				continuationAgent.Workspace,
				interactionContinuationSessionKey(record),
			),
			continuationAgent.ID,
		); err != nil {
			return result, err
		}
		result.Ownership = interactionInboundDeferred
		result.Effects.ContinuationQueued = true
		return result, nil
	}
	if record.Status != interactions.StatusWaiting {
		return result, fmt.Errorf(
			"interaction %q is not accepting input from status %q",
			record.ID,
			record.Status,
		)
	}
	if !command.Authorization.authorizes(record.Route) {
		return service.notice(
			ctx,
			command,
			result,
			"This session is waiting for an answer from the authorized user.",
		)
	}
	if interactionApprovalSupersededByInbound(record, command.Message) {
		message := service.runtime.prepareInboundMessageForAgent(ctx, command.Message)
		command.Message = message
		answer := interactions.Answer{
			Text:       message.Content,
			Media:      append([]string(nil), message.Media...),
			Superseded: true,
			MessageID:  strings.TrimSpace(message.Context.MessageID),
			ReceivedAt: time.Now().UnixMilli(),
		}
		claimed, err := registry.ClaimAnswer(
			record.ID,
			record.Revision,
			answer,
			interactions.OutcomeDenied,
		)
		if err != nil {
			if isInteractionAnswerConflict(err) {
				return service.notice(
					ctx,
					command,
					result,
					"This interaction changed while applying your new guidance; please retry.",
				)
			}
			return result, err
		}
		return service.resumeAcceptedAnswer(ctx, command, registry, claimed, result)
	}

	answerContent := service.runtime.interactionAnswerContent(record, command.Message)
	answer, err := parseInteractionAnswer(record, answerContent, command.Message.Context.MessageID)
	if err != nil {
		return service.notice(
			ctx,
			command,
			result,
			"I could not accept that answer: "+err.Error(),
		)
	}
	answer.ResponseMessageID = strings.TrimSpace(
		command.Message.Context.Raw[bus.InboundMetadataKeyInteractionResponseMessageID],
	)
	claimed, err := registry.ClaimAnswer(
		record.ID,
		record.Revision,
		answer,
		interactionAnswerOutcome(record, answer),
	)
	if err != nil {
		if isInteractionAnswerConflict(err) {
			return service.notice(
				ctx,
				command,
				result,
				"An answer is already being processed for this session.",
			)
		}
		return result, err
	}
	return service.resumeAcceptedAnswer(ctx, command, registry, claimed, result)
}

func isInteractionAnswerConflict(err error) bool {
	return errors.Is(err, interactions.ErrAnswerTooLate) || errors.Is(err, interactions.ErrDuplicateAnswer)
}

func (service interactionService) notice(
	ctx context.Context,
	command answerInteractionCommand,
	result answerInteractionResult,
	content string,
) (answerInteractionResult, error) {
	result.Admission = service.runtime.publishInteractionNoticeAdmission(
		ctx,
		command.Message,
		command.Authorization.SessionKey,
		content,
	)
	return result, nil
}

func (service interactionService) resumeAcceptedAnswer(
	ctx context.Context,
	command answerInteractionCommand,
	registry *interactions.Registry,
	claimed interactions.Record,
	result answerInteractionResult,
) (answerInteractionResult, error) {
	result.Record = claimed
	result.Ownership = interactionInboundClaimed
	result.Effects.AnswerPersisted = true
	service.runtime.syncInteractionControls(
		command.Workspace,
		claimed,
		bus.OutboundInteractionControlsRemove,
	)
	result.Effects.ControlsRemovalRequested = true
	if err := service.runtime.settleInboundAdmission(
		ctx,
		command.Message,
		finalResponseAdmission{status: finalResponseAdmissionNotRequired},
	); err != nil {
		return result, err
	}
	result.Effects.ResumeAttempted = true
	return result, service.resume(ctx, registry, command, claimed)
}

func (service interactionService) resume(
	ctx context.Context,
	registry *interactions.Registry,
	command answerInteractionCommand,
	record interactions.Record,
) error {
	resumeCommand, err := newResumeInteractionCommand(
		registry,
		command.Workspace,
		service.runtime.interactionContinuationAgent(record, command.Agent),
		command.Scope,
		command.Message.Context,
		record,
	)
	if err != nil {
		return err
	}
	_, err = service.Resume(ctx, resumeCommand)
	return err
}

func (service interactionService) Cancel(
	ctx context.Context,
	command cancelInteractionCommand,
) (interactionControlCancellationResult, error) {
	result := interactionControlCancellationResult{}
	runtime := service.runtime
	if runtime == nil || command.Target == nil || command.Agent == nil ||
		strings.TrimSpace(command.Workspace) == "" {
		return result, fmt.Errorf("interaction cancellation runtime is unavailable")
	}
	message := command.Message
	registry := runtime.interactionRegistryForWorkspace(command.Workspace)
	record, found := activeInteractionForSession(registry, command.Authorization.SessionKey)
	if !found || !command.Authorization.authorizes(record.Route) {
		return result, nil
	}
	projectedChoice := strings.TrimSpace(
		message.Context.Raw[bus.InboundMetadataKeyInteractionChoice],
	)
	projectedShortID := strings.TrimSpace(
		message.Context.Raw[bus.InboundMetadataKeyInteractionShortID],
	)
	if projectedChoice == bus.InboundInteractionChoiceCancel && projectedShortID == "" {
		return result, nil
	}
	if projectedShortID != "" && !strings.EqualFold(projectedShortID, record.ShortID) {
		return result, nil
	}
	if projectedChoice == bus.InboundInteractionChoiceCancel &&
		runtime.projectedInteractionPromptIdentity(
			record,
			projectedInteractionPromptMessageID(message),
		) != projectedInteractionPromptMatch {
		return result, nil
	}
	result.Matched = true
	result.TaskID = strings.TrimSpace(record.Origin.TaskID)
	result.Kind = record.Kind
	runInteractionLifecycleBoundaryHook(ctx, interactionBoundaryCancelAfterLoad)

	claimTurnID := fmt.Sprintf(
		"pending-interaction-cancel-%s-%d",
		record.ShortID,
		runtime.turns.nextSequence(),
	)
	claim, _, claimed := runtime.turns.claimRuntimeRouteSession(
		command.Target,
		claimTurnID,
	)
	if !claimed {
		current, err := service.beginCancellationFence(
			registry,
			record,
			command.Authorization,
			command.FailureCode,
		)
		if err != nil {
			result.Failed = true
			return result, err
		}
		record = current
		result.Effects.CancellationFenced = true
		result.TaskID = strings.TrimSpace(record.Origin.TaskID)
		result.Kind = record.Kind
		if err := service.abortContinuation(record, command.Agent); err != nil {
			result.Failed = true
			return result, fmt.Errorf("abort interaction continuation: %w", err)
		}
		result.Effects.ContinuationAbortRequested = true
		claim, claimed = service.waitForCancellationClaim(
			ctx,
			command.Target,
			claimTurnID,
		)
	}
	if !claimed {
		result.Failed = true
		return result, fmt.Errorf("interaction session is busy while canceling")
	}
	defer claim.releaseIfOwned()

	current, active := activeInteractionForSession(
		registry,
		command.Authorization.SessionKey,
	)
	if !active || current.ID != record.ID ||
		!command.Authorization.authorizes(current.Route) {
		result.Failed = true
		return result, fmt.Errorf("interaction changed while waiting to cancel")
	}
	record = current
	result.TaskID = strings.TrimSpace(record.Origin.TaskID)
	result.Kind = record.Kind
	if interactionFinalizationStarted(record) {
		result.Failed = true
		return result, fmt.Errorf("interaction finalization already started")
	}
	continuationAgent := runtime.interactionContinuationAgent(record, command.Agent)
	if continuationAgent != nil {
		runtime.turns.takePendingStop(newRuntimeSessionScope(
			continuationAgent.Workspace,
			interactionContinuationSessionKey(record),
		))
	}

	if record.Status != interactions.StatusCanceling {
		var err error
		record, err = registry.BeginCancellation(
			record.ID,
			record.Revision,
			command.FailureCode,
		)
		if err != nil {
			result.Failed = true
			return result, fmt.Errorf("begin %s cancellation: %w", command.ControlName, err)
		}
		result.Effects.CancellationFenced = true
	}
	runtime.syncInteractionControls(
		command.Workspace,
		record,
		bus.OutboundInteractionControlsRemove,
	)
	result.Effects.ControlsRemovalRequested = true
	if err := runtime.ensureInteractionCancellationToolResult(
		ctx,
		runtime.interactionContinuationAgent(record, command.Agent),
		record,
		record.FailureCode,
	); err != nil {
		result.Failed = true
		return result, fmt.Errorf("persist %s cancellation result: %w", command.ControlName, err)
	}
	result.Effects.ToolResultPersisted = true
	if err := runtime.failInteractionTask(
		command.Workspace,
		record,
		taskregistry.StatusCancelled,
		"human input was canceled",
	); err != nil {
		result.Failed = true
		return result, fmt.Errorf("cancel owning task: %w", err)
	}
	result.Effects.TaskCancelled = true
	completed, err := registry.CompleteCancellation(record.ID, record.Revision)
	if err != nil {
		result.Failed = true
		return result, fmt.Errorf("complete %s cancellation: %w", command.ControlName, err)
	}
	result.Effects.CancellationCompleted = true
	runtime.cleanupInteractionOriginTools(
		ctx,
		runtime.interactionContinuationAgent(completed, command.Agent),
		completed,
	)
	result.Effects.OriginCleanupRequested = true
	result.Canceled = true
	result.CommandHandled = command.ControlName == "stop"
	return result, nil
}

func (service interactionService) beginCancellationFence(
	registry *interactions.Registry,
	loaded interactions.Record,
	authorization interactionAuthorizationContext,
	code string,
) (interactions.Record, error) {
	for attempt := 0; attempt < interactionCancelFenceAttempts; attempt++ {
		current, active := activeInteractionForSession(registry, authorization.SessionKey)
		if !active || current.ID != loaded.ID ||
			!authorization.authorizes(current.Route) {
			return interactions.Record{}, fmt.Errorf("interaction changed while preparing cancellation")
		}
		if current.Status == interactions.StatusCanceling {
			return current, nil
		}
		if interactionFinalizationStarted(current) {
			return interactions.Record{}, fmt.Errorf("interaction finalization already started")
		}
		fenced, err := registry.BeginCancellation(current.ID, current.Revision, code)
		if err == nil {
			return fenced, nil
		}
		if !errors.Is(err, interactions.ErrConflict) {
			return interactions.Record{}, fmt.Errorf("begin cancellation fence: %w", err)
		}
	}
	return interactions.Record{}, fmt.Errorf("interaction kept changing while preparing cancellation")
}

func interactionFinalizationStarted(record interactions.Record) bool {
	return len(record.FinalDeliveryIDs) > 0
}

func (service interactionService) abortContinuation(
	record interactions.Record,
	fallbackAgent *AgentInstance,
) error {
	runtime := service.runtime
	agent := runtime.interactionContinuationAgent(record, fallbackAgent)
	if agent == nil {
		return fmt.Errorf("interaction continuation agent is unavailable")
	}
	scope := newRuntimeSessionScope(
		agent.Workspace,
		interactionContinuationSessionKey(record),
	)
	state := runtime.turns.activeTurnState(scope)
	if state == nil {
		runtime.turns.markPendingStop(scope)
		return nil
	}
	if strings.HasPrefix(state.snapshot().TurnID, pendingTurnPrefix) {
		runtime.turns.markPendingStop(scope)
		return nil
	}
	if err := runtime.hardAbortScope(scope); err != nil &&
		runtime.turns.activeTurnState(scope) != nil {
		return err
	}
	return nil
}

func (service interactionService) waitForCancellationClaim(
	ctx context.Context,
	target *inboundDispatchTarget,
	turnID string,
) (*runtimeSessionClaim, bool) {
	for attempt := 0; attempt < interactionCancelClaimAttempts; attempt++ {
		timer := time.NewTimer(interactionCancelClaimDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false
		case <-timer.C:
		}
		claim, _, claimed := service.runtime.turns.claimRuntimeRouteSession(target, turnID)
		if claimed {
			return claim, true
		}
	}
	return nil, false
}
