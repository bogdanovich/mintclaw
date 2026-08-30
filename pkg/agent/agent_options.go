package agent

import (
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/state"
)

// AgentLoopOption configures an AgentLoop at construction time.
type AgentLoopOption func(*AgentLoop)

// WithMediaStore injects the media store before context initialization. Call
// SetMediaStore after construction when registered tools must also receive it.
func WithMediaStore(store media.MediaStore) AgentLoopOption {
	return func(al *AgentLoop) {
		al.mediaStore = store
	}
}

// WithRuntimeEvents injects the runtime event bus used for new observation APIs.
//
// The injected bus is treated as externally owned and will not be closed by
// AgentLoop.Close. Passing nil leaves the default owned runtime bus enabled.
func WithRuntimeEvents(bus runtimeevents.Bus) AgentLoopOption {
	return func(al *AgentLoop) {
		if bus == nil {
			return
		}
		al.runtimeEvents = bus
		al.ownsRuntimeEvents = false
	}
}

// WithIsolatedToolBootstrap prevents shared production tools and their state
// managers from being constructed. Callers must provide an explicit tool
// allowlist and register every permitted tool after construction.
func WithIsolatedToolBootstrap() AgentLoopOption {
	return func(al *AgentLoop) {
		al.isolatedToolBootstrap = true
	}
}

// WithStateManager injects the runtime-owned current state manager before
// state-backed tools and model execution are constructed.
func WithStateManager(manager *state.Manager) AgentLoopOption {
	return func(al *AgentLoop) {
		if manager != nil {
			al.state = manager
		}
	}
}

func withCodingRuntimeProfile(profile CodingRuntimeProfile) AgentLoopOption {
	return func(al *AgentLoop) {
		al.codingProfile = &profile
	}
}

// WithIsolatedSkillBootstrap restricts every agent's skill loader to its own
// workspace. It prevents tests and evaluations from observing global or
// built-in skills installed in the host process environment.
func WithIsolatedSkillBootstrap() AgentLoopOption {
	return func(al *AgentLoop) {
		if al == nil || al.registry == nil || al.cfg == nil {
			return
		}
		al.isolatedSkillBootstrap = true
		al.isolateSkillRegistry(al.registry)
	}
}

func (al *AgentLoop) isolateSkillRegistry(registry *AgentRegistry) {
	if registry == nil {
		return
	}
	for _, agentID := range registry.ListAgentIDs() {
		instance, ok := registry.GetAgent(agentID)
		if !ok || instance == nil || instance.ContextBuilder == nil {
			continue
		}
		instance.ContextBuilder.isolateSkillBootstrap()
	}
}
