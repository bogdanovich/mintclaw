package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

// turnResourceCleanup settles resources once, before the terminal snapshot.
// The runner uses the same owner as a fallback on abnormal exits.
type turnResourceCleanup struct {
	once   sync.Once
	result tools.TurnCleanupResult
	err    error
}

func (c *turnResourceCleanup) settle(ctx context.Context, ts *turnState) (tools.TurnCleanupResult, error, bool) {
	performed := false
	c.once.Do(func() {
		performed = true
		if ts.agent == nil || ts.agent.Tools == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		c.result, c.err = ts.agent.Tools.CleanupTurnWithResult(toolExecutionContextForTurn(cleanupCtx, ts))
	})
	return c.result, c.err, performed
}

func prepareTerminalCleanup(
	ctx context.Context,
	ts *turnState,
	exec *turnExecution,
	terminal terminalContent,
) terminalContent {
	cleaned, err, _ := ts.cleanup.settle(ctx, ts)
	result := turnResult{finalContent: terminal.content, deliverable: exec.deliverable, receipts: exec.receipts}
	attachTurnCleanupReceipts(&result, cleaned.Receipts)
	if cleanupFailureAffectsTurn(err, ts) {
		const content = "The task's resource cleanup could not be confirmed. " +
			"Check the outcome before attempting further actions."
		if result.deliverable == nil {
			result.deliverable = &taskresult.Deliverable{}
		}
		result.deliverable = taskresult.CloneDeliverable(result.deliverable)
		result.finalContent = content
		result.deliverable.Text = content
		result.deliverable.Report = nil
		result.deliverable.ObjectiveOutcome = &taskresult.Outcome{
			Status:       taskresult.OutcomeBlocked,
			MissingItems: []string{"Confirm resource cleanup before treating the task as complete"},
			Explanation:  "The runtime could not confirm the final resource disposition.",
		}
	}
	exec.deliverable, exec.receipts = result.deliverable, result.receipts
	if result.finalContent != terminal.content {
		terminal = terminalContent{content: result.finalContent, persistIfToolHandled: true}
	}
	return terminal
}

// Cleanup visits every registered tool, including unused optional services.
// Only owners used by this execution or its durable continuation can block it.
// Invalid/unattributed cleanup evidence always fails closed.
func cleanupFailureAffectsTurn(err error, ts *turnState) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if cleanupFailureAffectsTurn(child, ts) {
				return true
			}
		}
		return false
	}
	var failure *tools.TurnCleanupError
	if errors.As(err, &failure) {
		for _, name := range ts.toolKindsSnapshot() {
			if name == failure.Tool {
				return true
			}
		}
		for _, receipt := range ts.opts.InitialReceipts {
			if receipt.Tool == failure.Tool {
				return true
			}
		}
		return ts.opts.InteractionContinuation.OriginToolName == failure.Tool
	}
	return true
}

func attachTurnCleanupReceipts(result *turnResult, receipts []taskresult.Receipt) {
	if result == nil || len(receipts) == 0 {
		return
	}
	result.receipts = mergeOutcomeReceipts(result.receipts, receipts)
	deliverable := taskresult.CloneDeliverable(result.deliverable)
	if deliverable == nil {
		deliverable = &taskresult.Deliverable{}
	}
	deliverable.LifecycleReceipts = mergeLifecycleReceipts(deliverable.LifecycleReceipts, receipts)
	// A terminal cleanup receipt means normal finalization ended while a
	// runtime-owned browser session was still live. Producer text and reports
	// were generated before that state transition and therefore cannot remain
	// authoritative. Fail closed with a receipt-backed, identifier-free result.
	if hasBrowserTerminalCleanupReceipt(receipts) {
		const content = "The browser session was closed by runtime cleanup because the turn ended without " +
			"an explicit close or durable handoff. The task's final result could not be verified safely. " +
			"Check the outcome before attempting further actions."
		result.finalContent = content
		deliverable.Text = content
		deliverable.Report = nil
		deliverable.ObjectiveOutcome = &taskresult.Outcome{
			Status: taskresult.OutcomeBlocked,
			MissingItems: []string{
				"Complete an explicit browser session close or durable handoff before finalizing the task",
			},
			Explanation: "Runtime cleanup closed a browser session that was still live at turn finalization.",
		}
	}
	result.deliverable = deliverable
}

func hasBrowserTerminalCleanupReceipt(receipts []taskresult.Receipt) bool {
	for _, receipt := range receipts {
		if receipt.Kind == taskresult.ReceiptKindResourceCleanup && receipt.Tool == "browser_session" &&
			receipt.Action == "close" {
			return true
		}
	}
	return false
}
