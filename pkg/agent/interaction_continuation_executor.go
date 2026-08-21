package agent

import (
	"context"
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
	approvedTool  *providers.ToolCall
	afterTool     func([]toolshared.WriteAuditEntry) error
	validateTool  func() error
	onAbort       func()
	approval      *ToolApprovalGrant
	origin        *bus.InboundContext
	resumeInbound *bus.InboundContext
	abortOnce     sync.Once
}

func (e *interactionContinuationExecutor) configure(opts *processOptions) {
	if e == nil || e.approvedTool == nil || opts == nil {
		return
	}
	e.resumeInbound = cloneInboundContext(opts.Dispatch.InboundContext)
	opts.ApprovalGrant = e.approval
	opts.Dispatch.InboundContext = cloneInboundContext(e.origin)
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
	host turnRuntimeHost,
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
		outcome, llm := pipeline.executeJournaledToolCall(turnCtx, ts, exec, *e.approvedTool)
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
		if ts.hardAbortRequested() || outcome.AbortCause == TurnAbortHard {
			e.abort()
			result, abortErr := host.abortTurn(ts)
			return result, TurnEndStatusAborted, abortErr
		}
		if outcome.Control == ToolControlSuspend {
			ts.setPhase(TurnPhaseSuspended)
			return turnResult{
				status:                 TurnEndStatusSuspended,
				suspendedInteractionID: outcome.SuspendedInteractionID,
			}, TurnEndStatusSuspended, nil
		}
		if e.validateTool != nil {
			if validateErr := e.validateTool(); validateErr != nil {
				return turnResult{}, TurnEndStatusError, validateErr
			}
		}
		ts.opts.ApprovalGrant = nil
		ts.opts.Dispatch.InboundContext = cloneInboundContext(e.resumeInbound)
		if outcome.Control == ToolControlBreak && llm.toolResponseDisposition == toolResponseHandled {
			result, finalizeErr := pipeline.finalizeTurn(
				turnCtx, ts, exec, llm, TurnEndStatusCompleted, "",
			)
			if finalizeErr != nil {
				return result, TurnEndStatusError, finalizeErr
			}
			return result, TurnEndStatusCompleted, nil
		}
	}

	return pipeline.runPreparedTurnLoop(ctx, turnCtx, ts, host, exec)
}

func (p *Pipeline) executeJournaledToolCall(
	turnCtx context.Context,
	ts *turnState,
	exec *turnExecution,
	toolCall providers.ToolCall,
) (ToolLoopOutcome, *LLMIterationState) {
	llm := newLLMIterationState(1)
	llm.response = &providers.LLMResponse{ToolCalls: []providers.ToolCall{toolCall}}
	llm.normalizedToolCalls = []providers.ToolCall{toolCall}
	llm.toolResponseDisposition = toolResponseHandled
	llm.assistantToolCallsPersisted = true
	outcome := p.ExecuteTools(turnCtx, turnCtx, ts, exec, llm)
	pauseCtx, pauseCancel := context.WithTimeout(context.WithoutCancel(turnCtx), 3*time.Second)
	p.pauseToolFeedbackForTurn(pauseCtx, ts)
	pauseCancel()
	return outcome, llm
}
