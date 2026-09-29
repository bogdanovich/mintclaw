package agent

import (
	"sort"
	"sync"
	"sync/atomic"
)

var runtimeCapabilityRevisionSequence atomic.Uint64

// runtimeCapabilityRevision is a small seqlock for the admission facts used
// by capability-filtered skill catalogs. Writers are serialized; odd values
// mean publication is in progress and even values identify stable snapshots.
type runtimeCapabilityRevision struct {
	mu       sync.Mutex
	sequence uint64
	value    atomic.Uint64
}

func newRuntimeCapabilityRevision() *runtimeCapabilityRevision {
	return &runtimeCapabilityRevision{sequence: runtimeCapabilityRevisionSequence.Add(1)}
}

func (revision *runtimeCapabilityRevision) current() uint64 {
	if revision == nil {
		return 0
	}
	return revision.value.Load()
}

// beginRuntimeCapabilityUpdate serializes a publication across every affected
// agent and marks each revision unstable before any admission state changes.
// The returned closure publishes the next stable generation and releases the
// writer locks.
func beginRuntimeCapabilityUpdate(agents ...*AgentInstance) func() {
	revisions := make([]*runtimeCapabilityRevision, 0, len(agents))
	seen := make(map[*runtimeCapabilityRevision]struct{}, len(agents))
	for _, agent := range agents {
		if agent == nil || agent.capabilityRevision == nil {
			continue
		}
		revision := agent.capabilityRevision
		if _, duplicate := seen[revision]; duplicate {
			continue
		}
		seen[revision] = struct{}{}
		revisions = append(revisions, revision)
	}
	sort.Slice(revisions, func(left, right int) bool {
		return revisions[left].sequence < revisions[right].sequence
	})
	for _, revision := range revisions {
		revision.mu.Lock()
		revision.value.Add(1)
	}
	return func() {
		for _, revision := range revisions {
			revision.value.Add(1)
		}
		for index := len(revisions) - 1; index >= 0; index-- {
			revisions[index].mu.Unlock()
		}
	}
}

func registryAgents(registry *AgentRegistry) []*AgentInstance {
	if registry == nil {
		return nil
	}
	agents := make([]*AgentInstance, 0, len(registry.ListAgentIDs()))
	for _, agentID := range registry.ListAgentIDs() {
		if agent, ok := registry.GetAgent(agentID); ok && agent != nil {
			agents = append(agents, agent)
		}
	}
	return agents
}
