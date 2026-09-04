package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// interactionContinuationExecutor owns both continuation modes. Questions enter
// the prepared model loop directly; approvals first execute the journaled tool
// call and then continue through that same setup, lifecycle, and cleanup.
type interactionContinuationExecutor struct {
	approvedTool *providers.ToolCall
	afterTool    func([]toolshared.WriteAuditEntry) error
	validateTool func() error
	onAbort      func()
	approval     *ToolApprovalGrant
	origin       *bus.InboundContext
	abortOnce    sync.Once
}

func (e *interactionContinuationExecutor) configure(opts *turnSpec) {
	if e == nil || e.approvedTool == nil || opts == nil {
		return
	}
	opts.ApprovalGrant = e.approval
}

func (e *interactionContinuationExecutor) abort() {
	if e == nil || e.onAbort == nil {
		return
	}
	e.abortOnce.Do(e.onAbort)
}

func (e *interactionContinuationExecutor) execute(
	ctx context.Context,
	turnCtx context.Context,
	ts *turnState,
	pipeline *Pipeline,
) (turnResult, TurnEndStatus, error) {
	exec, err := pipeline.SetupTurn(turnCtx, ts)
	if err != nil {
		return turnResult{}, TurnEndStatusError, err
	}
	if exec.model.cleanup != nil {
		defer exec.model.cleanup()
	}

	if e != nil && e.approvedTool != nil {
		outcome, llm := pipeline.executeJournaledToolCall(
			turnCtx, ts, exec, *e.approvedTool, e.origin,
		)
		if e.afterTool != nil {
			if afterErr := e.afterTool(exec.writeAudit); afterErr != nil {
				return turnResult{}, TurnEndStatusError, afterErr
			}
		}
		if outcome.TurnErr != nil {
			return turnResult{}, TurnEndStatusError, outcome.TurnErr
		}
		if outcome.JournalErr != nil {
			return turnResult{}, TurnEndStatusError, outcome.JournalErr
		}
		if outcome.Control == turnStepSuspend {
			ts.setPhase(TurnPhaseSuspended)
			return turnResult{
				status:                 TurnEndStatusSuspended,
				suspendedInteractionID: outcome.SuspendedInteractionID,
			}, TurnEndStatusSuspended, nil
		}
		if ts.hardAbortRequested() {
			e.abort()
			result, abortErr := pipeline.abortTurn(ts)
			return result, TurnEndStatusAborted, abortErr
		}
		if outcome.Control == turnStepAbort {
			switch outcome.AbortCause {
			case turnAbortHard:
				e.abort()
				result, abortErr := pipeline.abortTurn(ts)
				return result, TurnEndStatusAborted, abortErr
			case turnAbortHook:
				return turnResult{}, TurnEndStatusError, fmt.Errorf("hook requested turn abort")
			default:
				return turnResult{}, TurnEndStatusError, fmt.Errorf("tool phase returned abort without a cause")
			}
		}
		if outcome.AbortCause != turnAbortNone {
			return turnResult{}, TurnEndStatusError, fmt.Errorf("tool phase returned an abort cause without aborting")
		}
		if e.validateTool != nil {
			if validateErr := e.validateTool(); validateErr != nil {
				return turnResult{}, TurnEndStatusError, validateErr
			}
		}
		if repairErr := repairJournaledToolPair(
			exec, ts.agent.Sessions.GetHistory(ts.sessionKey), e.approvedTool.ID,
		); repairErr != nil {
			return turnResult{}, TurnEndStatusError, repairErr
		}
		ts.consumeApprovalGrant()
		if outcome.Control == turnStepFinalize {
			terminal := pipeline.completeTerminal(
				turnCtx,
				ts,
				exec,
				llm,
				TurnEndStatusCompleted,
				toolTerminalRequest(outcome, llm, terminalContent{}),
			)
			if !terminal.resume {
				return terminal.result, terminal.status, terminal.err
			}
		}
	}

	return pipeline.runPreparedTurnLoop(ctx, turnCtx, ts, exec)
}

func repairJournaledToolPair(
	exec *turnExecution,
	history []providers.Message,
	toolCallID string,
) error {
	originIndex, resultIndex := interactionToolPairIndexes(history, toolCallID)
	if originIndex < 0 || resultIndex < 0 {
		return fmt.Errorf("journaled tool pair %q is incomplete", toolCallID)
	}

	providerOriginIndex := -1
	providerResultIndex := -1
	for index, message := range exec.messages {
		if messageContainsToolCall(message, toolCallID) {
			providerOriginIndex = index
		}
		if message.Role == "tool" && message.ToolCallID == toolCallID {
			providerResultIndex = index
		}
	}
	if providerResultIndex < 0 {
		return fmt.Errorf("provider context is missing live tool result %q", toolCallID)
	}
	liveResult := providerPromptMessageForTurn(exec.messages[providerResultIndex])

	if providerOriginIndex < 0 {
		repaired := make([]providers.Message, 0, len(exec.messages)+resultIndex-originIndex)
		for _, message := range exec.messages {
			if message.Role != "tool" || message.ToolCallID != toolCallID {
				repaired = append(repaired, message)
			}
		}
		for _, message := range history[originIndex : resultIndex+1] {
			if message.Role == "tool" && message.ToolCallID == toolCallID {
				repaired = append(repaired, liveResult)
			} else {
				repaired = append(repaired, providerPromptMessageForTurn(message))
			}
		}
		exec.messages = repaired
		return nil
	}

	repaired := make([]providers.Message, 0, len(exec.messages))
	for index, message := range exec.messages {
		if index > providerOriginIndex && message.Role == "tool" && message.ToolCallID == toolCallID {
			continue
		}
		repaired = append(repaired, message)
		if index == providerOriginIndex {
			repaired = append(repaired, liveResult)
		}
	}
	exec.messages = repaired
	return nil
}

func messageContainsToolCall(message providers.Message, toolCallID string) bool {
	if message.Role != "assistant" {
		return false
	}
	for _, toolCall := range message.ToolCalls {
		if toolCall.ID == toolCallID {
			return true
		}
	}
	return false
}

func (p *Pipeline) executeJournaledToolCall(
	turnCtx context.Context,
	ts *turnState,
	exec *turnExecution,
	toolCall providers.ToolCall,
	origin *bus.InboundContext,
) (ToolLoopOutcome, *LLMIterationState) {
	llm := newLLMIterationState(1)
	llm.response = &providers.LLMResponse{ToolCalls: []providers.ToolCall{toolCall}}
	llm.normalizedToolCalls = []providers.ToolCall{toolCall}
	llm.toolResponseDisposition = toolResponseHandled
	llm.assistantToolCallsPersisted = true
	executionCtx := withApprovedToolExecutionIdentity(turnCtx, origin)
	outcome := p.ExecuteTools(executionCtx, executionCtx, ts, exec, llm)
	pauseCtx, pauseCancel := context.WithTimeout(context.WithoutCancel(turnCtx), 3*time.Second)
	p.pauseToolFeedbackForTurn(pauseCtx, ts)
	pauseCancel()
	return outcome, llm
}

type approvedToolExecutionIdentityKey struct{}

func withApprovedToolExecutionIdentity(
	ctx context.Context,
	origin *bus.InboundContext,
) context.Context {
	if origin == nil {
		return ctx
	}
	return context.WithValue(ctx, approvedToolExecutionIdentityKey{}, cloneInboundContext(origin))
}
