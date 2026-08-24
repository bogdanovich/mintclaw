package agent

import (
	"context"

	"github.com/bogdanovich/mintclaw/pkg/agent/interfaces"
)

func newTestPipeline(al *AgentLoop) *Pipeline {
	return newTurnRunner(al, al.GetConfig()).pipeline
}

func runTestTurn(
	al *AgentLoop,
	ctx context.Context,
	ts *turnState,
	pipeline *Pipeline,
) (turnResult, error) {
	return (&turnRunner{runtime: al.turns, pipeline: pipeline, traceCapture: al.traceCapture}).run(ctx, ts, nil)
}

func ensureTestTurnRunner(al *AgentLoop) {
	if al.turns == nil {
		al.turns = newTurnRuntime(al.registry, al.bus)
	}
	if al.turns.currentRunner() == nil {
		al.turns.replaceRunner(newTurnRunner(al, al.cfg))
	}
}

func setTestContextManager(al *AgentLoop, manager ContextManager) {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.contextManager = manager
	al.turns.replaceRunner(newTurnRunner(al, al.cfg))
}

func setTestMessageBus(al *AgentLoop, messageBus interfaces.MessageBus) {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.bus = messageBus
	if al.turns == nil {
		al.turns = newTurnRuntime(al.registry, messageBus)
		return
	}
	if al.turns.inbound == nil {
		al.turns.inbound = &inboundSpool{}
	}
	al.turns.inbound.bus = messageBus
	if al.turns.currentRunner() != nil {
		al.turns.replaceRunner(newTurnRunner(al, al.cfg))
	}
}
