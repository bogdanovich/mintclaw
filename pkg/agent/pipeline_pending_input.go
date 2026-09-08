package agent

import (
	"context"
	"fmt"

	"github.com/bogdanovich/mintclaw/pkg/logger"
)

type pendingInputInjection struct {
	count           int
	totalContentLen int
}

// injectPendingTurnInputs advances the turn-owned FIFO one message at a time.
// The queue head is committed only after canonical persistence and insertion
// into the next model context both succeed. A failed head and every untouched
// suffix therefore remain owned by turnExecution for explicit settlement.
func (p *Pipeline) injectPendingTurnInputs(
	turnCtx context.Context,
	ts *turnState,
	exec *turnExecution,
	mediaResolver mediaResolver,
	maxMediaSize int,
) (pendingInputInjection, error) {
	var outcome pendingInputInjection
	pending := exec.pendingInputs.snapshotEntries()
	if len(pending) == 0 {
		return outcome, nil
	}
	messages := exec.pendingInputs.Snapshot()
	resolved := resolveMediaRefs(
		messages,
		mediaResolver,
		p.Context.CodingMedia,
		maxMediaSize,
		0,
	)
	for index := range pending {
		input := pending[index]
		message := input.message
		providerMessage := providerPromptMessageForTurn(resolved[index])
		var writeErr error
		if !ts.opts.NoHistory {
			writeErr = persistFullSessionMessage(turnCtx, ts.agent.Sessions, ts.sessionKey, &message)
			if !canonicalMessageAppendCommitted(writeErr) {
				return outcome, fmt.Errorf("persist pending turn input: %w", writeErr)
			}
			ts.recordPersistedMessage(message)
			p.ingestMessage(turnCtx, ts, message, writeErr)
		}

		exec.messages = append(exec.messages, providerMessage)
		if input.kind == turnPendingSteering && exec.shouldTrackTurnOwnedSteering(message) {
			ts.recordAcceptedSteeringMessage(message)
		}
		if !exec.pendingInputs.CommitFront() {
			return outcome, fmt.Errorf("commit pending turn input: queue head is unavailable")
		}
		outcome.count++
		outcome.totalContentLen += len(providerMessage.Content)
		logger.InfoCF("agent", "Injected pending input into context", map[string]any{
			"agent_id":    ts.agent.ID,
			"iteration":   ts.currentIteration(),
			"content_len": len(providerMessage.Content),
			"media_count": len(message.Media),
		})
		if writeErr != nil {
			return outcome, fmt.Errorf("persist pending turn input: %w", writeErr)
		}
	}
	return outcome, nil
}

// settlePendingTurnInputsAfterFailure closes active steering admission,
// captures any steer that won admission before the close, and transfers every
// unconsumed steering message into turnState. turnRunner can then release its
// durable inbound ownership on the terminal error instead of losing a local
// batch or replaying an uncertain append.
func (p *Pipeline) settlePendingTurnInputsAfterFailure(ts *turnState, exec *turnExecution) {
	if ts == nil || exec == nil {
		return
	}
	exec.pendingInputs.AppendSteering(p.drainAndSealSteeringAdmission(ts)...)
	for _, input := range exec.pendingInputs.Drain() {
		if input.kind != turnPendingSteering || !exec.shouldTrackTurnOwnedSteering(input.message) {
			continue
		}
		ts.recordAcceptedSteeringMessage(input.message)
	}
}
