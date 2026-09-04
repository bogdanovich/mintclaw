package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/session"
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
	return service.runtime.resumeClaimedInteraction(
		ctx,
		registry,
		command.Workspace,
		service.runtime.interactionContinuationAgent(record, command.Agent),
		command.Scope,
		command.Message.Context,
		record,
	)
}
