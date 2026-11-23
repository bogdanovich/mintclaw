package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
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
	finish := exec.continuationDecision.finished()
	unconfirmed := cleanupFailureAffectsTurn(err, ts)
	language := ts.opts.InteractionContinuation.PromptLanguage
	if finish {
		attachTurnLifecycleReceipts(&result, cleaned.Receipts)
		content, verified := liveHandoffFinishMessage(language, ts.opts.InitialReceipts, cleaned.Receipts)
		result.finalContent = content
		unconfirmed = unconfirmed || !verified
	} else {
		attachTurnCleanupReceipts(&result, cleaned.Receipts)
	}
	if finish || unconfirmed {
		result.deliverable = taskresult.CloneDeliverable(result.deliverable)
		if result.deliverable == nil {
			result.deliverable = &taskresult.Deliverable{}
		}
		if unconfirmed {
			result.finalContent = "The task's resource cleanup could not be confirmed. " +
				"Check the outcome before attempting further actions."
			if finish {
				result.finalContent = interactions.PromptText(language, interactions.PromptLiveHandoffUnconfirmed)
			}
			result.deliverable.ObjectiveOutcome = &taskresult.Outcome{
				Status:       taskresult.OutcomeBlocked,
				MissingItems: []string{"Confirm resource cleanup before treating the task as complete"},
				Explanation:  "The runtime could not confirm the final resource disposition.",
			}
		}
		result.deliverable.Text = result.finalContent
		result.deliverable.Report = nil
	}
	exec.deliverable, exec.receipts = result.deliverable, result.receipts
	if result.finalContent != terminal.content {
		terminal = terminalContent{content: result.finalContent, persistIfToolHandled: true}
	}
	return terminal
}

// Only terminal effects attested by the owning tools qualify for a status claim.
func liveHandoffFinishMessage(language string, handoffs, cleaned []taskresult.Receipt) (string, bool) {
	pendingOwners := make(map[string]struct{})
	for _, handoff := range handoffs {
		if handoff.Kind == taskresult.ObjectiveKindLiveHandoff {
			pendingOwners[handoff.Tool] = struct{}{}
		}
	}
	closed, released := false, false
	for _, receipt := range cleaned {
		if receipt.Kind != taskresult.ReceiptKindResourceCleanup {
			continue
		}
		switch {
		case receipt.Action == "close" && receipt.Metadata["state"] == "closed":
			closed = true
		case receipt.Action == "release" && receipt.Metadata["state"] == "released":
			released = true
		default:
			return "", false
		}
		delete(pendingOwners, receipt.Tool)
	}
	if len(pendingOwners) > 0 || !closed && !released {
		return "", false
	}
	parts := []string{interactions.PromptText(language, interactions.PromptLiveHandoffFinished)}
	if closed {
		parts = append(parts, interactions.PromptText(language, interactions.PromptLiveHandoffClosed))
	}
	if released {
		parts = append(parts, interactions.PromptText(language, interactions.PromptLiveHandoffReleased))
	}
	return strings.Join(parts, " "), true
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

func attachTurnLifecycleReceipts(result *turnResult, receipts []taskresult.Receipt) {
	if result == nil || len(receipts) == 0 {
		return
	}
	result.receipts = mergeOutcomeReceipts(result.receipts, receipts)
	deliverable := taskresult.CloneDeliverable(result.deliverable)
	if deliverable == nil {
		deliverable = &taskresult.Deliverable{}
	}
	deliverable.LifecycleReceipts = mergeLifecycleReceipts(deliverable.LifecycleReceipts, receipts)
	result.deliverable = deliverable
}

func attachTurnCleanupReceipts(result *turnResult, receipts []taskresult.Receipt) {
	attachTurnLifecycleReceipts(result, receipts)
	if result == nil || !hasBrowserTerminalCleanupReceipt(receipts) {
		return
	}
	// A terminal cleanup receipt means normal finalization ended while a
	// runtime-owned browser session was still live. Producer text and reports
	// were generated before that state transition and therefore cannot remain
	// authoritative. Fail closed with a receipt-backed, identifier-free result.
	deliverable := result.deliverable
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

func hasBrowserTerminalCleanupReceipt(receipts []taskresult.Receipt) bool {
	for _, receipt := range receipts {
		if receipt.Kind == taskresult.ReceiptKindResourceCleanup && receipt.Tool == "browser_session" &&
			receipt.Action == "close" {
			return true
		}
	}
	return false
}
