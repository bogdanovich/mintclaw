package agent

import (
	"context"
	"fmt"
	"strings"

	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type terminalRequest struct {
	content    terminalContent
	renderMode terminalRenderMode
}

type terminalGatewayOutcome struct {
	result turnResult
	status TurnEndStatus
	resume bool
	err    error
}

func toolTerminalRequest(
	outcome ToolLoopOutcome,
	llm *LLMIterationState,
	fallback terminalContent,
) terminalRequest {
	content := fallback
	if outcome.TerminalMode == terminalRenderExact {
		content = exactTerminalContent(outcome.FinalContent)
	} else if strings.TrimSpace(outcome.FinalContent) != "" {
		content = terminalContent{content: outcome.FinalContent}
	}
	if llm != nil && llm.toolResponseDisposition == toolResponseHandled &&
		outcome.TerminalMode != terminalRenderExact {
		content = terminalContent{}
	}
	return terminalRequest{content: content, renderMode: outcome.TerminalMode}
}

func (p *Pipeline) runTurnLoop(
	ctx context.Context,
	turnCtx context.Context,
	ts *turnState,
) (turnResult, TurnEndStatus, error) {
	exec, err := p.SetupTurn(turnCtx, ts)
	if err != nil {
		return turnResult{}, TurnEndStatusError, err
	}
	defer func() {
		if exec != nil && exec.model.cleanup != nil {
			exec.model.cleanup()
		}
	}()
	return p.runPreparedTurnLoop(ctx, turnCtx, ts, exec)
}

func (p *Pipeline) runPreparedTurnLoop(
	ctx context.Context,
	turnCtx context.Context,
	ts *turnState,
	exec *turnExecution,
) (turnResult, TurnEndStatus, error) {
	turnStatus := TurnEndStatusCompleted
	maxMediaSize := p.maxMediaSize()
	mediaResolver := p.Context.MediaResolver
	llm := newLLMIterationState(0)
	terminalRequested := false

	for {
		graceful, _ := ts.gracefulInterruptRequested()
		canRun := ts.currentIteration() < ts.agent.MaxIterations || len(exec.pendingMessages) > 0 || graceful ||
			exec.objectiveRepairPending
		if terminalRequested || (!canRun && !p.continueWithPendingSubTurnResults(ts, exec)) {
			if exec.terminal.content == "" {
				if ts.currentIteration() >= ts.agent.MaxIterations && ts.agent.MaxIterations > 0 {
					exec.terminal = terminalContent{content: toolLimitResponse}
				} else {
					exec.terminal = terminalContent{content: ts.opts.DefaultResponse}
				}
			}
			terminal := p.completeTerminal(
				turnCtx,
				ts,
				exec,
				llm,
				turnStatus,
				terminalRequest{content: exec.terminal},
			)
			if terminal.resume {
				terminalRequested = false
				continue
			}
			return terminal.result, terminal.status, terminal.err
		}
		if ts.hardAbortRequested() {
			turnStatus = TurnEndStatusAborted
			result, abortErr := p.abortTurn(ts)
			return result, turnStatus, abortErr
		}

		iteration := ts.currentIteration() + 1
		ts.setIteration(iteration)
		ts.setPhase(TurnPhaseRunning)
		if exec.objectiveRepairPending {
			exec.objectiveRepairPending = false
			exec.objectiveRepairActive = true
		}
		repairIteration := exec.objectiveRepairActive

		var pendingMessages []providers.Message
		if !repairIteration {
			pendingMessages = append([]providers.Message(nil), exec.pendingMessages...)
		}
		if len(pendingMessages) > 0 {
			exec.markSteeringObserved()
			exec.pendingMessages = nil
		}
		if !repairIteration && iteration == 1 && !ts.opts.mode.skipsInitialSteeringPoll() {
			if steerMsgs := p.dequeueSteeringMessagesForTurn(ts); len(steerMsgs) > 0 {
				exec.markSteeringObserved()
				pendingMessages = append(pendingMessages, steerMsgs...)
			}
		}

		// Check if parent turn has ended.
		if ts.IsParentEnded() {
			if !ts.critical {
				logger.InfoCF(
					"agent",
					"Parent turn ended, non-critical SubTurn exiting gracefully",
					map[string]any{
						"agent_id":  ts.agentID,
						"iteration": iteration,
						"turn_id":   ts.turnID,
					},
				)
				terminalRequested = true
				continue
			}
			logger.InfoCF(
				"agent",
				"Parent turn ended, critical SubTurn continues running",
				map[string]any{
					"agent_id":  ts.agentID,
					"iteration": iteration,
					"turn_id":   ts.turnID,
				},
			)
		}

		// Poll for pending SubTurn results.
		if !repairIteration {
			if result, ok := ts.dequeuePendingResult(); ok && result != nil && result.ForLLM != "" {
				content := p.filterPendingResultForLLM(result.ForLLM)
				msg := subTurnResultPromptMessage(content)
				pendingMessages = append(pendingMessages, msg)
			}
		}

		// Inject pending steering messages
		if len(pendingMessages) > 0 {
			resolvedPending := resolveMediaRefs(
				pendingMessages,
				mediaResolver,
				p.Context.CodingMedia,
				maxMediaSize,
				0,
			)
			totalContentLen := 0
			for i, pm := range pendingMessages {
				providerMsg := providerPromptMessageForTurn(resolvedPending[i])
				exec.messages = append(exec.messages, providerMsg)
				totalContentLen += len(providerMsg.Content)
				if !ts.opts.NoHistory {
					writeErr := persistFullSessionMessage(turnCtx, ts.agent.Sessions, ts.sessionKey, &pm)
					if writeErr != nil {
						turnStatus = TurnEndStatusError
						return turnResult{}, turnStatus, fmt.Errorf("persist steering message: %w", writeErr)
					}
					ts.recordPersistedMessage(pm)
					p.ingestMessage(turnCtx, ts, pm, nil)
				}
				if exec.shouldTrackTurnOwnedSteering(pm) {
					ts.recordAcceptedSteeringMessage(pm)
				}
				logger.InfoCF("agent", "Injected steering message into context",
					map[string]any{
						"agent_id":    ts.agent.ID,
						"iteration":   iteration,
						"content_len": len(providerMsg.Content),
						"media_count": len(pm.Media),
					})
			}
			p.emitEvent(
				runtimeevents.KindAgentSteeringInjected,
				ts.eventMeta("runTurn", "turn.steering.injected"),
				SteeringInjectedPayload{
					Count:           len(pendingMessages),
					TotalContentLen: totalContentLen,
				},
			)
			// Clear exec.pendingMessages after injection so InitialSteeringMessages
			// are not re-injected on subsequent iterations (Issue 2 fix).
			exec.pendingMessages = nil
		}
		logger.DebugCF("agent", "LLM iteration",
			map[string]any{
				"agent_id":  ts.agent.ID,
				"iteration": iteration,
				"max":       ts.agent.MaxIterations,
			})

		// Execute LLM call via Pipeline
		ts.setPhase(TurnPhaseRunning)
		llm = newLLMIterationState(iteration)
		llmOutcome, callErr := p.CallLLM(ctx, turnCtx, ts, exec, llm)
		if repairIteration {
			exec.objectiveRepairActive = false
		}
		if callErr != nil {
			turnStatus = TurnEndStatusError
			return turnResult{}, turnStatus, callErr
		}
		if llmOutcome.Control == turnStepAbort {
			switch llmOutcome.AbortCause {
			case turnAbortHard:
				turnStatus = TurnEndStatusAborted
				result, abortErr := p.abortTurn(ts)
				return result, turnStatus, abortErr
			case turnAbortHook:
				turnStatus = TurnEndStatusError
				return turnResult{}, turnStatus, fmt.Errorf("hook requested turn abort")
			default:
				turnStatus = TurnEndStatusError
				return turnResult{}, turnStatus, fmt.Errorf("model phase returned abort without a cause")
			}
		}
		if llmOutcome.AbortCause != turnAbortNone {
			turnStatus = TurnEndStatusError
			return turnResult{}, turnStatus, fmt.Errorf("model phase returned an abort cause without aborting")
		}
		exec.terminal = llmOutcome.terminalCandidate(exec.terminal)
		if repairIteration {
			repairCandidate := llmOutcome.terminalCandidate(terminalContent{})
			if repaired := strings.TrimSpace(repairCandidate.content); repaired != "" {
				repairedMessage := providers.Message{Role: "assistant", Content: repairCandidate.content}
				exec.messages = append(exec.messages, repairedMessage)
				exec.objectiveRepairMessages = append(exec.objectiveRepairMessages, repairedMessage)
			}
			if steerMsgs := p.dequeueSteeringMessagesForTurn(ts); len(steerMsgs) > 0 {
				exec.markSteeringObserved()
				exec.pendingMessages = append(exec.pendingMessages, steerMsgs...)
			}
			if result, ok := ts.dequeuePendingResult(); ok && result != nil && result.ForLLM != "" {
				content := p.filterPendingResultForLLM(result.ForLLM)
				exec.pendingMessages = append(exec.pendingMessages, subTurnResultPromptMessage(content))
			}
			if len(exec.pendingMessages) > 0 {
				continue
			}
		}

		switch llmOutcome.Control {
		case turnStepContinue:
			continue
		case turnStepFinalize:
			// Ensure empty response falls back to DefaultResponse.
			if exec.terminal.content == "" {
				exec.terminal = terminalContent{content: ts.opts.DefaultResponse}
			}
			terminal := p.completeTerminal(
				turnCtx,
				ts,
				exec,
				llm,
				turnStatus,
				terminalRequest{content: exec.terminal},
			)
			if terminal.resume {
				continue
			}
			return terminal.result, terminal.status, terminal.err
		case turnStepExecuteTools:
			// Execute tools via Pipeline
			toolOutcome := p.ExecuteTools(ctx, turnCtx, ts, exec, llm)
			if toolOutcome.TurnErr != nil {
				turnStatus = TurnEndStatusError
				return turnResult{}, turnStatus, toolOutcome.TurnErr
			}
			if toolOutcome.JournalErr != nil {
				turnStatus = TurnEndStatusError
				return turnResult{}, turnStatus, toolOutcome.JournalErr
			}
			if toolOutcome.Control != turnStepAbort && toolOutcome.AbortCause != turnAbortNone {
				turnStatus = TurnEndStatusError
				return turnResult{}, turnStatus, fmt.Errorf("tool phase returned an abort cause without aborting")
			}
			switch toolOutcome.Control {
			case turnStepContinue:
				// ExecuteTools already appended model-visible results to exec.messages.
				continue
			case turnStepSuspend:
				turnStatus = TurnEndStatusSuspended
				ts.setPhase(TurnPhaseSuspended)
				return turnResult{
					status:                 turnStatus,
					suspendedInteractionID: toolOutcome.SuspendedInteractionID,
				}, turnStatus, nil
			case turnStepFinalize:
				terminal := p.completeTerminal(
					turnCtx,
					ts,
					exec,
					llm,
					turnStatus,
					toolTerminalRequest(toolOutcome, llm, exec.terminal),
				)
				if terminal.resume {
					continue
				}
				return terminal.result, terminal.status, terminal.err
			case turnStepAbort:
				switch toolOutcome.AbortCause {
				case turnAbortHard:
					turnStatus = TurnEndStatusAborted
					result, abortErr := p.abortTurn(ts)
					return result, turnStatus, abortErr
				case turnAbortHook:
					turnStatus = TurnEndStatusError
					return turnResult{}, turnStatus, fmt.Errorf("hook requested turn abort")
				default:
					turnStatus = TurnEndStatusError
					return turnResult{}, turnStatus, fmt.Errorf("tool phase returned abort without a cause")
				}
			default:
				turnStatus = TurnEndStatusError
				return turnResult{}, turnStatus, fmt.Errorf("tool phase returned unknown step %d", toolOutcome.Control)
			}
		default:
			turnStatus = TurnEndStatusError
			return turnResult{}, turnStatus, fmt.Errorf("model phase returned unknown step %d", llmOutcome.Control)
		}
	}
}

func (p *Pipeline) completeTerminal(
	turnCtx context.Context,
	ts *turnState,
	exec *turnExecution,
	llm *LLMIterationState,
	status TurnEndStatus,
	request terminalRequest,
) terminalGatewayOutcome {
	if ts.hardAbortRequested() {
		result, err := p.abortTurn(ts)
		return terminalGatewayOutcome{result: result, status: TurnEndStatusAborted, err: err}
	}

	exec.terminal = request.content
	if request.renderMode == terminalRenderExact {
		if strings.TrimSpace(exec.terminal.content) == "" {
			exec.terminal.content = "The tool loop was stopped by runtime safety protection."
		}
	} else {
		if p.continueWithPendingSubTurnResults(ts, exec) {
			return terminalGatewayOutcome{status: status, resume: true}
		}

		rendered, ok := tryRenderFinalTurnReply(turnCtx, p.Cfg, ts, exec, exec.terminal)
		exec.terminal = rendered
		if request.renderMode == terminalRenderRequired && !ok {
			return terminalGatewayOutcome{status: status, resume: true}
		}

		if steerMsgs := p.dequeueSteeringMessagesForTurn(ts); len(steerMsgs) > 0 {
			exec.markSteeringObserved()
			logger.InfoCF(
				"agent",
				"Steering arrived during terminal render; continuing turn",
				map[string]any{
					"agent_id":       ts.agent.ID,
					"iteration":      ts.currentIteration(),
					"steering_count": len(steerMsgs),
				},
			)
			exec.pendingMessages = append(exec.pendingMessages, steerMsgs...)
			return terminalGatewayOutcome{status: status, resume: true}
		}
		if p.continueWithPendingSubTurnResults(ts, exec) {
			return terminalGatewayOutcome{status: status, resume: true}
		}
		if p.scheduleObjectiveOutcomeRepair(turnCtx, ts, exec, llm, exec.terminal) {
			exec.terminal = terminalContent{}
			return terminalGatewayOutcome{status: status, resume: true}
		}
	}

	if ts.hardAbortRequested() {
		result, err := p.abortTurn(ts)
		return terminalGatewayOutcome{result: result, status: TurnEndStatusAborted, err: err}
	}
	result, err := p.finalizeTurn(turnCtx, ts, exec, llm, status, exec.terminal)
	if err != nil {
		status = TurnEndStatusError
	}
	return terminalGatewayOutcome{result: result, status: status, err: err}
}

func (p *Pipeline) scheduleObjectiveOutcomeRepair(
	turnCtx context.Context,
	ts *turnState,
	exec *turnExecution,
	llm *LLMIterationState,
	terminal terminalContent,
) bool {
	if exec == nil || exec.objectiveRepairAttempted || len(ts.opts.ObjectiveChecklist) == 0 ||
		strings.TrimSpace(terminal.content) == "" {
		return false
	}
	instruction, repair := objectiveOutcomeRepairInstruction(
		terminal.content,
		exec.writeAudit,
		ts.opts.ObjectiveChecklist,
	)
	if !repair {
		return false
	}
	cancelConfiguredStreamingLLM(turnCtx, llm)
	exec.objectiveRepairAttempted = true
	exec.objectiveRepairPending = true
	exec.objectiveRepairTailIndex = len(ts.liveTurnMessagesSnapshot())
	exec.objectiveRepairMessages = []providers.Message{
		{Role: "assistant", Content: terminal.content},
		{Role: "user", Content: instruction},
	}
	exec.messages = append(exec.messages, exec.objectiveRepairMessages...)
	logger.WarnCF("agent", "Scheduled objective finalization repair", map[string]any{
		"agent_id":  ts.agent.ID,
		"iteration": ts.currentIteration(),
	})
	return true
}

func (p *Pipeline) continueWithPendingSubTurnResults(
	ts *turnState,
	exec *turnExecution,
) bool {
	for {
		results, sealed := ts.sealOrDrainPendingResults()
		if sealed {
			return false
		}
		appended := false
		for _, result := range results {
			if result == nil || result.ForLLM == "" {
				continue
			}
			content := p.filterPendingResultForLLM(result.ForLLM)
			msg := subTurnResultPromptMessage(content)
			exec.pendingMessages = append(exec.pendingMessages, msg)
			appended = true
		}
		if appended {
			return true
		}
	}
}
