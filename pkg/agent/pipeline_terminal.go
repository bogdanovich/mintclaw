package agent

import (
	"context"
	"strings"

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
		if request.renderMode != terminalRenderRequired && p.continueWithPendingSubTurnResults(ts, exec) {
			return terminalGatewayOutcome{status: status, resume: true}
		}

		rendered, ok := tryRenderFinalTurnReply(turnCtx, p.Cfg, ts, exec, exec.terminal)
		exec.terminal = rendered
		if request.renderMode == terminalRenderRequired && !ok {
			return terminalGatewayOutcome{status: status, resume: true}
		}

		if steerMsgs := p.dequeueSteeringMessagesForTurn(ts); len(steerMsgs) > 0 {
			cancelConfiguredStreamingLLM(turnCtx, llm)
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
