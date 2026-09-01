package agent

import (
	"context"
	"fmt"
	"strings"

	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

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
	messages := exec.messages
	maxMediaSize := p.maxMediaSize()
	finalContent := terminalContent{}
	mediaResolver := p.Context.MediaResolver
	llm := newLLMIterationState(0)

	for {
		graceful, _ := ts.gracefulInterruptRequested()
		canRun := ts.currentIteration() < ts.agent.MaxIterations || len(exec.pendingMessages) > 0 || graceful ||
			exec.objectiveRepairPending
		if !canRun && !p.continueWithPendingSubTurnResults(ts, exec) {
			break
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
		if ts.parentTurnState != nil && ts.IsParentEnded() {
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
				break
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
		if !repairIteration && ts.pendingResults != nil {
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
				messages = append(messages, providerMsg)
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
		// Always sync messages into exec.messages so CallLLM sees the updated state
		exec.messages = messages

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
		if llmOutcome.AbortCause == TurnAbortHard {
			turnStatus = TurnEndStatusAborted
			result, abortErr := p.abortTurn(ts)
			return result, turnStatus, abortErr
		}
		if llmOutcome.AbortCause == TurnAbortHook {
			turnStatus = TurnEndStatusError
			return turnResult{}, turnStatus, fmt.Errorf("hook requested turn abort")
		}
		messages = exec.messages
		finalContent = llmOutcome.terminalCandidate(finalContent)
		if repairIteration {
			repairCandidate := llmOutcome.terminalCandidate(terminalContent{})
			if repaired := strings.TrimSpace(repairCandidate.content); repaired != "" {
				repairedMessage := providers.Message{Role: "assistant", Content: repairCandidate.content}
				exec.messages = append(exec.messages, repairedMessage)
				exec.objectiveRepairMessages = append(exec.objectiveRepairMessages, repairedMessage)
				messages = exec.messages
			}
			if steerMsgs := p.dequeueSteeringMessagesForTurn(ts); len(steerMsgs) > 0 {
				exec.markSteeringObserved()
				exec.pendingMessages = append(exec.pendingMessages, steerMsgs...)
			}
			if ts.pendingResults != nil {
				if result, ok := ts.dequeuePendingResult(); ok && result != nil && result.ForLLM != "" {
					content := p.filterPendingResultForLLM(result.ForLLM)
					exec.pendingMessages = append(exec.pendingMessages, subTurnResultPromptMessage(content))
				}
			}
			if len(exec.pendingMessages) > 0 {
				continue
			}
		}

		switch llmOutcome.Control {
		case ControlContinue:
			continue
		case ControlBreak:
			// Ensure empty response falls back to DefaultResponse
			if finalContent.content == "" {
				finalContent = terminalContent{content: ts.opts.DefaultResponse}
			}
			if p.continueWithPendingSubTurnResults(ts, exec) {
				messages = exec.messages
				continue
			}
			finalContent = renderFinalTurnReply(turnCtx, p.Cfg, ts, exec, finalContent)
			if p.scheduleObjectiveOutcomeRepair(turnCtx, ts, exec, llm, finalContent) {
				messages = exec.messages
				continue
			}
			result, finalizeErr := p.finalizeTurn(
				turnCtx,
				ts,
				exec,
				llm,
				turnStatus,
				finalContent,
			)
			if finalizeErr != nil {
				turnStatus = TurnEndStatusError
			}
			return result, turnStatus, finalizeErr
		case ControlToolLoop:
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
			switch toolOutcome.Control {
			case ToolControlContinue:
				// Re-read exec.messages since ExecuteTools may have updated it
				// (added tool results/skipped messages) before returning ControlContinue
				messages = exec.messages
				continue
			case ToolControlFinalize:
				renderedContent, rendered := tryRenderFinalTurnReply(
					turnCtx,
					p.Cfg,
					ts,
					exec,
					finalContent,
				)
				finalContent = renderedContent
				if !rendered {
					messages = exec.messages
					continue
				}
				if steerMsgs := p.dequeueSteeringMessagesForTurn(ts); len(steerMsgs) > 0 {
					exec.markSteeringObserved()
					logger.InfoCF(
						"agent",
						"Steering arrived during terminal render; continuing turn",
						map[string]any{
							"agent_id":       ts.agent.ID,
							"iteration":      iteration,
							"steering_count": len(steerMsgs),
						},
					)
					exec.pendingMessages = append(exec.pendingMessages, steerMsgs...)
					messages = exec.messages
					continue
				}
				if p.continueWithPendingSubTurnResults(ts, exec) {
					messages = exec.messages
					continue
				}
				if p.scheduleObjectiveOutcomeRepair(turnCtx, ts, exec, llm, finalContent) {
					messages = exec.messages
					continue
				}
				result, finalizeErr := p.finalizeTurn(turnCtx, ts, exec, llm, turnStatus, finalContent)
				if finalizeErr != nil {
					turnStatus = TurnEndStatusError
				}
				return result, turnStatus, finalizeErr
			case ToolControlSuspend:
				turnStatus = TurnEndStatusSuspended
				ts.setPhase(TurnPhaseSuspended)
				return turnResult{
					status:                 turnStatus,
					suspendedInteractionID: toolOutcome.SuspendedInteractionID,
				}, turnStatus, nil
			case ToolControlHalt:
				finalContent = terminalContent{content: toolOutcome.FinalContent}
				if strings.TrimSpace(finalContent.content) == "" {
					finalContent.content = "The tool loop was stopped by runtime safety protection."
				}
				result, finalizeErr := p.finalizeTurn(
					turnCtx,
					ts,
					exec,
					llm,
					turnStatus,
					finalContent,
				)
				if finalizeErr != nil {
					turnStatus = TurnEndStatusError
				}
				return result, turnStatus, finalizeErr
			case ToolControlBreak:
				if toolOutcome.AbortCause == TurnAbortHard {
					turnStatus = TurnEndStatusAborted
					result, abortErr := p.abortTurn(ts)
					return result, turnStatus, abortErr
				}
				if toolOutcome.AbortCause == TurnAbortHook {
					turnStatus = TurnEndStatusError
					return turnResult{}, turnStatus, fmt.Errorf("hook requested turn abort")
				}
				// ExecuteTools returned ControlBreak. A handled tool response suppresses
				// DefaultResponse; otherwise use outcome content when present.
				if strings.TrimSpace(toolOutcome.FinalContent) != "" {
					finalContent = terminalContent{content: toolOutcome.FinalContent}
				}
				if llm.toolResponseDisposition == toolResponseHandled {
					finalContent = terminalContent{}
				}
				if p.continueWithPendingSubTurnResults(ts, exec) {
					messages = exec.messages
					continue
				}
				finalContent = renderFinalTurnReply(turnCtx, p.Cfg, ts, exec, finalContent)
				if p.scheduleObjectiveOutcomeRepair(turnCtx, ts, exec, llm, finalContent) {
					messages = exec.messages
					continue
				}
				result, finalizeErr := p.finalizeTurn(
					turnCtx,
					ts,
					exec,
					llm,
					turnStatus,
					finalContent,
				)
				if finalizeErr != nil {
					turnStatus = TurnEndStatusError
				}
				return result, turnStatus, finalizeErr
			}
		}
	}

	if ts.hardAbortRequested() {
		turnStatus = TurnEndStatusAborted
		result, abortErr := p.abortTurn(ts)
		return result, turnStatus, abortErr
	}

	if finalContent.content == "" {
		if ts.currentIteration() >= ts.agent.MaxIterations && ts.agent.MaxIterations > 0 {
			finalContent = terminalContent{content: toolLimitResponse}
		} else {
			finalContent = terminalContent{content: ts.opts.DefaultResponse}
		}
	}
	finalContent = renderFinalTurnReply(turnCtx, p.Cfg, ts, exec, finalContent)
	if p.scheduleObjectiveOutcomeRepair(turnCtx, ts, exec, llm, finalContent) {
		return p.runPreparedTurnLoop(ctx, turnCtx, ts, exec)
	}

	// Check hard abort before finalizing (may have been set during tool execution)
	if ts.hardAbortRequested() {
		turnStatus = TurnEndStatusAborted
		result, abortErr := p.abortTurn(ts)
		return result, turnStatus, abortErr
	}

	result, err := p.finalizeTurn(turnCtx, ts, exec, llm, turnStatus, finalContent)
	if err != nil {
		turnStatus = TurnEndStatusError
	}
	return result, turnStatus, err
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
