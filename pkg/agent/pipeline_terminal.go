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
		p.sealSteeringAdmission(ts)
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

		rendered, ok := tryRenderFinalTurnReply(turnCtx, p.turnPolicy.finalTurnRender, ts, exec, exec.terminal)
		exec.terminal = rendered
		if request.renderMode == terminalRenderRequired && !ok {
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

	if p.continueWithSteeringAtExit(turnCtx, ts, exec, llm, "terminal transition") {
		return terminalGatewayOutcome{status: status, resume: true}
	}

	if ts.hardAbortRequested() {
		p.sealSteeringAdmission(ts)
		result, err := p.abortTurn(ts)
		return terminalGatewayOutcome{result: result, status: TurnEndStatusAborted, err: err}
	}
	result, err := p.finalizeTurn(turnCtx, ts, exec, llm, status, exec.terminal)
	if err != nil {
		status = TurnEndStatusError
	}
	return terminalGatewayOutcome{result: result, status: status, err: err}
}

// continueWithSteeringAtExit is the single non-cancellation exit gateway for
// active coding guidance. It either moves every admitted message into the
// current turn's next iteration or seals admission before the caller exits.
func (p *Pipeline) continueWithSteeringAtExit(
	turnCtx context.Context,
	ts *turnState,
	exec *turnExecution,
	llm *LLMIterationState,
	reason string,
) bool {
	hadAdmittedPending := exec.pendingInputs.Len() > 0
	steerMessages := p.dequeueOrSealSteeringAtExit(ts, hadAdmittedPending)
	if len(steerMessages) == 0 && !hadAdmittedPending {
		return false
	}
	cancelConfiguredStreamingLLM(turnCtx, llm)
	if len(steerMessages) > 0 {
		exec.markSteeringObserved()
		exec.pendingInputs.AppendSteering(steerMessages...)
	}
	logger.InfoCF(
		"agent",
		"Steering arrived during turn exit; continuing turn",
		map[string]any{
			"agent_id":       ts.agent.ID,
			"iteration":      ts.currentIteration(),
			"reason":         reason,
			"pending_count":  exec.pendingInputs.Len(),
			"steering_count": len(steerMessages),
		},
	)
	return true
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
			exec.pendingInputs.AppendSubTurn(msg)
			appended = true
		}
		if appended {
			return true
		}
	}
}
