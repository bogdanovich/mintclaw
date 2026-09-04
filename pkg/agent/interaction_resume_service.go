package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
)

type resumeInteractionCommand struct {
	Registry  *interactions.Registry
	Workspace string
	Agent     *AgentInstance
	Scope     *session.SessionScope
	Inbound   bus.InboundContext
	Record    interactions.Record
}

type interactionResumeEffects struct {
	SingleFlightOwned         bool
	SingleFlightJoined        bool
	SteeringHandoffConfigured bool
	ContinuationStarted       bool
	FinalizationCompleted     bool
}

type resumeInteractionResult struct {
	Record  interactions.Record
	Effects interactionResumeEffects
}

func newResumeInteractionCommand(
	registry *interactions.Registry,
	workspace string,
	agent *AgentInstance,
	scope *session.SessionScope,
	inbound bus.InboundContext,
	record interactions.Record,
) (resumeInteractionCommand, error) {
	if registry == nil || agent == nil {
		return resumeInteractionCommand{}, fmt.Errorf("interaction continuation runtime is unavailable")
	}
	return resumeInteractionCommand{
		Registry:  registry,
		Workspace: workspace,
		Agent:     agent,
		Scope:     session.CloneScope(scope),
		Inbound:   *cloneInboundContext(&inbound),
		Record:    record,
	}, nil
}

func (service interactionService) Resume(
	ctx context.Context,
	command resumeInteractionCommand,
) (resumeInteractionResult, error) {
	result := resumeInteractionResult{Record: command.Record}
	runtime := service.runtime
	if runtime == nil || command.Registry == nil || command.Agent == nil {
		return result, fmt.Errorf("interaction continuation runtime is unavailable")
	}
	for {
		flightKey, flight, owner := runtime.startInteractionResumeFlight(
			command.Workspace,
			command.Record.ID,
		)
		if !owner {
			result.Effects.SingleFlightJoined = true
			select {
			case <-ctx.Done():
				return result, ctx.Err()
			case <-flight.done:
				if flight.handled {
					service.refreshResumeResult(&result, command)
					return result, flight.err
				}
				continue
			}
		}
		result.Effects.SingleFlightOwned = true
		var resumeErr error
		defer func() {
			runtime.finishInteractionResumeFlight(flightKey, flight, true, resumeErr)
		}()
		if err := configureInteractionSteeringHandoff(
			flight,
			command.Workspace,
			command.Record,
			command.Agent,
		); err != nil {
			resumeErr = err
			return result, resumeErr
		}
		result.Effects.SteeringHandoffConfigured = true
		result.Effects.ContinuationStarted = true
		resumeErr = service.resumeOwned(ctx, command)
		service.refreshResumeResult(&result, command)
		return result, resumeErr
	}
}

func (service interactionService) refreshResumeResult(
	result *resumeInteractionResult,
	command resumeInteractionCommand,
) {
	current, ok := command.Registry.Get(command.Record.ID)
	if !ok {
		return
	}
	result.Record = current
	switch current.Status {
	case interactions.StatusResolved, interactions.StatusCancelled, interactions.StatusFailed:
		result.Effects.FinalizationCompleted = true
	}
}

func (service interactionService) resumeOwned(
	ctx context.Context,
	command resumeInteractionCommand,
) error {
	runtime := service.runtime
	registry := command.Registry
	interactionWorkspace := command.Workspace
	agent := command.Agent
	scope := command.Scope
	inbound := command.Inbound
	record := command.Record
	current, ok := registry.Get(record.ID)
	if !ok {
		return interactions.ErrNotFound
	}
	switch current.Status {
	case interactions.StatusResolved, interactions.StatusCancelled, interactions.StatusFailed:
		return nil
	case interactions.StatusClaimed, interactions.StatusResuming:
		record = current
	default:
		return fmt.Errorf("cannot resume interaction from status %q", current.Status)
	}
	continuationSessionKey := interactionContinuationSessionKey(record)
	continuationScope := sessionScopeForRecovery(agent.Sessions, continuationSessionKey)
	if continuationScope == nil {
		continuationScope = session.CloneScope(scope)
	}
	if continuationScope != nil {
		if continuationScope.RouteScopeKey == "" && scope != nil {
			continuationScope.RouteScopeKey = scope.RouteScopeKey
		}
		if continuationScope.RouteScopeKey == "" {
			continuationScope.RouteScopeKey = record.Route.RouteSessionKey
			if continuationScope.RouteScopeKey == "" {
				continuationScope.RouteScopeKey = record.Route.SessionKey
			}
		}
		// Approval replies arrive on the parent route, but execution resumes in
		// the agent that owns the durable continuation.
		continuationScope.AgentID = agent.ID
		if strings.TrimSpace(record.Origin.TaskID) != "" {
			continuationScope.ClientSessionID = ""
			if err := clearSessionClientIDs(agent.Sessions, continuationSessionKey); err != nil {
				return fmt.Errorf("clear durable task frontend mappings: %w", err)
			}
		}
		ensureSessionMetadata(agent.Sessions, continuationSessionKey, continuationScope)
	}
	approvalAllowed := record.Kind == interactions.KindApproval &&
		record.Outcome == interactions.OutcomeAllowed
	if !approvalAllowed {
		if err := runtime.ensureInteractionToolResult(ctx, agent, record); err != nil {
			_, _ = registry.RecordResumeFailure(record.ID, record.Revision, err.Error())
			return err
		}
	}
	supersedingSteering := interactionSupersedingSteering(
		record,
		agent.Sessions.GetHistory(continuationSessionKey),
	)
	resuming := record
	continuationExecutor := &interactionContinuationExecutor{}
	if record.Status == interactions.StatusClaimed {
		var err error
		resuming, err = registry.MarkResuming(record.ID, record.Revision)
		if err != nil {
			return err
		}
	} else if record.Status != interactions.StatusResuming {
		return fmt.Errorf("cannot resume interaction from status %q", record.Status)
	}
	if approvalAllowed {
		current, ok := registry.Get(resuming.ID)
		if !ok {
			return interactions.ErrNotFound
		}
		resuming = current
		if _, resultIndex := interactionToolPairIndexes(
			agent.Sessions.GetHistory(continuationSessionKey),
			resuming.Origin.ToolCallID,
		); resultIndex < 0 {
			if resuming.ApprovalConsumedAt != 0 {
				outcome := interactions.OutcomeDeliveryUnknown
				text := "The one-time approval was consumed before restart, but tool execution " +
					"could not be confirmed. The tool was not retried."
				if receiptIDs := interactionOutcomeReceiptIDs(resuming); len(receiptIDs) > 0 {
					outcome = interactions.OutcomeAllowed
					text = "The approved external action completed before restart. Verified runtime receipt IDs: " +
						strings.Join(receiptIDs, ", ") + "."
				} else {
					var markErr error
					resuming, markErr = registry.MarkApprovalDeliveryUnknown(resuming.ID, resuming.Revision)
					if markErr != nil {
						return markErr
					}
				}
				if err := runtime.persistInteractionToolResult(
					ctx,
					agent,
					resuming,
					interactionToolResultPayload{
						InteractionID: resuming.ID,
						Outcome:       outcome,
						Text:          text,
					},
				); err != nil {
					return err
				}
			} else {
				executor, err := runtime.prepareApprovedInteractionTool(ctx, registry, agent, resuming)
				if err != nil {
					_, _ = registry.RecordResumeFailure(resuming.ID, resuming.Revision, err.Error())
					return err
				}
				continuationExecutor = executor
			}
		}
		current, ok = registry.Get(resuming.ID)
		if !ok {
			return interactions.ErrNotFound
		}
		if current.Status == interactions.StatusResolved {
			return nil
		}
		resuming = current
	}
	if finalContent, recoveredDeliverable, ok := interactionFinalAfterToolResult(
		agent.Sessions.GetHistory(continuationSessionKey),
		record.Origin.ToolCallID,
	); ok {
		cleanContent, objectiveOutcome := extractResumedObjectiveOutcome(
			finalContent, interactionOutcomeAudits(resuming), resuming,
		)
		runtime.sealActiveInteractionSteeringHandoff(interactionWorkspace, resuming.ID)
		_, finalizeErr := service.finalizeResumedInteraction(
			ctx, registry, interactionWorkspace, resuming, inbound, cleanContent,
			terminalTurnDeliverable(recoveredDeliverable, cleanContent, objectiveOutcome), nil,
			interactionBoundaryPrecomputedFinal,
		)
		return finalizeErr
	}

	routeSessionKey := record.Route.RouteSessionKey
	if routeSessionKey == "" {
		routeSessionKey = record.Route.SessionKey
	}
	modelBinding := runtime.bindEffectiveModel(routeSessionKey, agent)
	defer modelBinding.Cleanup()
	expectFinalDelivery := runtime.interactionContinuationExpectsUserDelivery(
		interactionWorkspace, record,
	)
	var deliveryObservation *finalDeliveryObservation
	if expectFinalDelivery {
		deliveryObservation = &finalDeliveryObservation{}
	}
	dispatch := DispatchRequest{
		RouteSessionKey: routeSessionKey,
		BaseSessionKey:  continuationSessionKey,
		SessionKey:      continuationSessionKey,
		InboundContext:  cloneInboundContext(&inbound),
		SessionScope:    session.CloneScope(continuationScope),
	}
	continuationOpts := newTurnSpec(turnModeInteractionContinuation, dispatch, modelBinding)
	continuationOpts.TaskID = record.Origin.TaskID
	continuationOpts.InteractionWorkspace = interactionWorkspace
	continuationOpts.InteractionSessionKey = record.Route.SessionKey
	continuationOpts.InteractionRouteKey = routeSessionKey
	continuationOpts.InteractionOriginExecution = record.Origin.ExecutionID
	continuationOpts.InteractionOriginContext = cloneInboundContext(record.Origin.ExecutionContext)
	continuationOpts.ObjectiveChecklist = runtimeObjectiveChecklist(record.Origin.ObjectiveChecklist)
	continuationOpts.ExpectFinalDelivery = deliveryObservation != nil
	continuationOpts.FinalDeliveryObservation = deliveryObservation
	continuationOpts.InitialSteeringMessages = supersedingSteering
	continuationExecutor.configure(&continuationOpts)
	resumedTurn, runErr := runtime.runAgentLoopWithExecution(
		ctx, agent, continuationOpts, continuationExecutor.execute,
	)
	if resumedTurn.status == TurnEndStatusAborted {
		continuationExecutor.abort()
	}
	if runErr != nil {
		_, _ = registry.RecordResumeFailure(resuming.ID, resuming.Revision, runErr.Error())
		return runErr
	}
	if resumedTurn.status == TurnEndStatusSuspended {
		return nil
	}
	if resumedTurn.status == TurnEndStatusAborted {
		return nil
	}
	audits := interactionOutcomeAudits(resuming)
	audits = appendTurnWriteAudit(audits, "", resumedTurn.writeAudit)
	finalContent, objectiveOutcome := extractResumedObjectiveOutcome(
		resumedTurn.finalContent, audits, resuming,
	)
	runtime.sealActiveInteractionSteeringHandoff(interactionWorkspace, resuming.ID)
	var traceScopes []runtimeevents.TraceScope
	if deliveryObservation != nil {
		traceScopes = deliveryObservation.traceScopes
	}
	finalization, deliveryErr := service.finalizeResumedInteraction(
		ctx,
		registry,
		interactionWorkspace,
		resuming,
		inbound,
		finalContent,
		terminalTurnDeliverable(resumedTurn.deliverable, finalContent, objectiveOutcome),
		traceScopes,
		interactionBoundaryModelFinal,
	)
	if deliveryObservation != nil {
		admission := finalResponseAdmission{status: finalResponseAdmissionAccepted}
		if finalization == interactionFinalizationCanceled {
			admission = rejectedFinalResponseAdmission(errInteractionFinalizationCanceled)
		} else if deliveryErr != nil {
			admission = rejectedFinalResponseAdmission(deliveryErr)
		}
		if settleErr := runtime.settleSteeringMessages(
			admission,
			deliveryObservation.takeUnsettledSteering(),
		); settleErr != nil {
			deliveryErr = errors.Join(deliveryErr, settleErr)
		}
	}
	return deliveryErr
}

func (service interactionService) finalizeResumedInteraction(
	ctx context.Context,
	registry *interactions.Registry,
	interactionWorkspace string,
	record interactions.Record,
	inbound bus.InboundContext,
	content string,
	deliverable *taskresult.Deliverable,
	traceScopes []runtimeevents.TraceScope,
	boundary string,
) (interactionFinalizationDisposition, error) {
	runInteractionLifecycleBoundaryHook(ctx, boundary)
	current, ok := registry.Get(record.ID)
	if !ok {
		return interactionFinalizationDelivered, interactions.ErrNotFound
	}
	switch current.Status {
	case interactions.StatusCanceling, interactions.StatusCancelled:
		return interactionFinalizationCanceled, nil
	case interactions.StatusResuming:
		return interactionFinalizationDelivered, service.runtime.deliverInteractionFinal(
			ctx, registry, interactionWorkspace, current, inbound, content, deliverable, traceScopes,
		)
	default:
		return interactionFinalizationDelivered, fmt.Errorf(
			"cannot finalize interaction from status %q", current.Status,
		)
	}
}
