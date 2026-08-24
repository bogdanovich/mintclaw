package agent

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/bogdanovich/mintclaw/pkg/agent/interfaces"
	"github.com/bogdanovich/mintclaw/pkg/routing"
)

// turnRuntime owns the mutable state shared by every turn entrypoint. A loop
// has exactly one turn runtime; config reloads replace only its immutable
// runner generation.
type turnRuntime struct {
	admissions          *agentTurnAdmissionController
	activeRequests      *activeRequestCounter
	inbound             *inboundSpool
	activeTurnStates    sync.Map
	activeRouteSessions sync.Map
	pendingStops        sync.Map
	sequence            atomic.Uint64

	runnerMu sync.RWMutex
	runner   *turnRunner
}

func newTurnRuntime(registry *AgentRegistry, messageBus interfaces.MessageBus) *turnRuntime {
	return &turnRuntime{
		admissions:     newAgentTurnAdmissionController(registry),
		activeRequests: newActiveRequestCounter(),
		inbound:        &inboundSpool{bus: messageBus},
	}
}

func (r *turnRuntime) currentRunner() *turnRunner {
	if r == nil {
		return nil
	}
	r.runnerMu.RLock()
	defer r.runnerMu.RUnlock()
	return r.runner
}

func (r *turnRuntime) replaceRunner(runner *turnRunner) {
	if r == nil {
		return
	}
	r.runnerMu.Lock()
	r.runner = runner
	r.runnerMu.Unlock()
}

func (r *turnRuntime) nextSequence() uint64 {
	if r == nil {
		return 0
	}
	return r.sequence.Add(1)
}

func (r *turnRuntime) acquireAgentTurn(
	ctx context.Context,
	agentID string,
) (context.Context, func(), error) {
	return r.acquireAgentTurnObserved(ctx, agentID, nil)
}

func (r *turnRuntime) acquireAgentTurnObserved(
	ctx context.Context,
	agentID string,
	onWait func(active, limit int),
) (context.Context, func(), error) {
	agentID = routing.NormalizeAgentID(agentID)
	if agentID == "" || r == nil || r.admissions == nil {
		return ctx, func() {}, nil
	}
	if admissions, ok := ctx.Value(agentTurnAdmissionsKey{}).(map[string]*agentTurnAdmissionLease); ok {
		if admissions[agentID] != nil {
			return ctx, func() {}, nil
		}
	}

	release, err := r.admissions.acquireObserved(ctx, agentID, onWait)
	if err != nil {
		return ctx, nil, err
	}

	lease := newAgentTurnAdmissionLease(release)
	admissions := cloneAgentTurnAdmissions(ctx)
	admissions[agentID] = lease
	admittedCtx := context.WithValue(ctx, agentTurnAdmissionsKey{}, admissions)
	return admittedCtx, lease.releaseRef, nil
}
