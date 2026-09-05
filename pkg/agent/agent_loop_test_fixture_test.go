package agent

import (
	"sync"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

// agentLoopTestFixture is the canonical construction seam for tests that need
// a real loop generation. Tests should customize config before construction
// instead of populating private AgentLoop fields directly.
type agentLoopTestFixture struct {
	Loop   *AgentLoop
	Agent  *AgentInstance
	Config *config.Config
	Bus    *bus.MessageBus

	closeOnce sync.Once
}

func newAgentLoopTestFixture(
	t *testing.T,
	provider providers.LLMProvider,
	configure ...func(*config.Config),
) *agentLoopTestFixture {
	t.Helper()
	return newAgentLoopTestFixtureWithWorkspace(t, t.TempDir(), provider, configure...)
}

func newAgentLoopTestFixtureWithWorkspace(
	t *testing.T,
	workspace string,
	provider providers.LLMProvider,
	configure ...func(*config.Config),
) *agentLoopTestFixture {
	t.Helper()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         workspace,
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
				ContextManager:    "none",
			},
		},
	}
	for _, apply := range configure {
		apply(cfg)
	}
	messageBus := bus.NewMessageBus()
	loop := NewAgentLoop(cfg, messageBus, provider)
	loop.interactions.catalog = interactions.NewWorkspaceCatalog(t.TempDir())
	fixture := &agentLoopTestFixture{
		Loop: loop, Agent: loop.registry.GetDefaultAgent(), Config: cfg, Bus: messageBus,
	}
	if fixture.Agent == nil {
		fixture.Close()
		t.Fatal("expected default agent")
	}
	t.Cleanup(fixture.Close)
	return fixture
}

func (fixture *agentLoopTestFixture) Close() {
	if fixture == nil {
		return
	}
	fixture.closeOnce.Do(func() {
		if fixture.Loop != nil {
			fixture.Loop.Close()
		}
	})
}

// turnState constructs mutable turn state through the same freeze boundary as
// production. Tests may shape turnSpec, but do not need to mirror turnState.
func (fixture *agentLoopTestFixture) turnState(spec turnSpec) *turnState {
	spec = normalizeTurnSpec(spec)
	inbound := spec.Dispatch.InboundContext
	scope := fixture.Loop.newTurnEventScope(
		fixture.Agent.ID,
		fixture.Agent.Workspace,
		spec.Dispatch.SessionKey,
		&TurnContext{Inbound: inbound},
	)
	return newTurnState(fixture.Agent, spec, scope)
}
