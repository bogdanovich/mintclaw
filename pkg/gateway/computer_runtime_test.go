package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

func TestComputerRuntimeToolRegistrationRequiresExplicitAgentGrant(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			client, _, cfg := computerClientFixtureConfig(t, 0)
			cfg.Tools.Computer.Enabled = enabled
			cfg.Agents.List = []config.AgentConfig{{ID: "browser", Default: true}, {ID: "reader"}}
			messageBus := bus.NewMessageBus()
			t.Cleanup(messageBus.Close)
			loop := agent.NewAgentLoop(cfg, messageBus, &startupBlockedProvider{reason: "must not call the model"})
			t.Cleanup(loop.Close)
			running := &services{NodeAdmission: client.source.runtime}
			if err := setupComputerTools(cfg, loop, running); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := closeComputerRuntime(t.Context(), running); err != nil {
					t.Error(err)
				}
			}()
			for _, toolName := range []string{"computer_targets", "computer_session", "computer_observe"} {
				_, visible := loop.GetRegistry().GetDefaultAgent().Tools.Get(toolName)
				if visible != enabled {
					t.Fatal("computer tools did not follow explicit enablement", toolName)
				}
				other, found := loop.GetRegistry().GetAgent("reader")
				if !found {
					t.Fatal("ungranted agent unavailable")
				}
				if _, visible := other.Tools.Get(toolName); visible {
					t.Fatal("ungranted agent received computer authority", toolName)
				}
			}
		})
	}
}

func computerRuntimeFixture(t *testing.T) (*services, *gatewayComputerToolSource, *computerClientTestHandler) {
	t.Helper()
	client, handler := computerClientFixture(t, 0)
	runtime := &computerRuntime{
		client:      client,
		revision:    client.policy.PolicyRevision(client.execution),
		workspaceID: client.workspaceID,
	}
	running := &services{Computer: runtime, NodeAdmission: client.source.runtime}
	source := &gatewayComputerToolSource{
		services:    running,
		revision:    runtime.revision,
		workspaceID: runtime.workspaceID,
	}
	return running, source, handler
}

func TestComputerRuntimeDisabledNeedsNoNodeAuthority(t *testing.T) {
	running := &services{}
	if err := setupComputerRuntime(config.DefaultConfig(), running); err != nil || running.Computer != nil {
		t.Fatal("default configuration created native desktop authority", err)
	}
	client, _, cfg := computerClientFixtureConfig(t, 0)
	if err := setupComputerRuntime(cfg, running); !errors.Is(err, computer.ErrUnavailable) {
		t.Fatal("enabled configuration without transport succeeded", err)
	}
	running.NodeAdmission = client.source.runtime
	if err := setupComputerRuntime(cfg, running); err != nil || running.Computer == nil {
		t.Fatal("explicit configured runtime was not created", err)
	}
	if err := setupComputerRuntime(cfg, running); !errors.Is(err, computer.ErrBusy) {
		t.Fatal("live runtime was silently replaced", err)
	}
	if err := closeComputerRuntime(t.Context(), running); err != nil || running.Computer != nil {
		t.Fatal("unused runtime did not release", err)
	}
}

func TestComputerRuntimeRejectsRetiredPolicyAndWorkspaceWithoutDispatch(t *testing.T) {
	running, source, handler := computerRuntimeFixture(t)
	if !source.Available() {
		t.Fatal("current runtime unavailable")
	}
	for _, change := range []string{"policy", "workspace"} {
		t.Run(change, func(t *testing.T) {
			retired := *source
			if change == "policy" {
				retired.revision = "retired"
			} else {
				retired.workspaceID = "different"
			}
			if retired.Available() {
				t.Fatal("retired authority appears available")
			}
			if _, err := retired.Targets(
				t.Context(),
				computerClientPrincipal(),
				"retired-"+change,
			); !errors.Is(
				err,
				computer.ErrStale,
			) {
				t.Fatal("retired tool source dispatched", err)
			}
		})
	}
	if len(handler.commands) != 0 {
		t.Fatal("retired authority reached companion")
	}
	if err := closeComputerRuntime(t.Context(), running); err != nil {
		t.Fatal(err)
	}
	if source.Available() {
		t.Fatal("closed runtime remained available")
	}
}

func TestComputerRuntimeLocksHonorCancellation(t *testing.T) {
	running, source, _ := computerRuntimeFixture(t)
	running.computerMu.Lock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := source.OwnedSessions(ctx, computerClientPrincipal())
	running.computerMu.Unlock()
	if !errors.Is(err, context.Canceled) {
		t.Fatal("source lock ignored cancellation", err)
	}
	running.computerMu.RLock()
	err = closeComputerRuntime(ctx, running)
	running.computerMu.RUnlock()
	if !errors.Is(err, context.Canceled) || running.Computer == nil {
		t.Fatal("canceled shutdown discarded runtime ownership", err)
	}
}

func TestComputerRuntimeUnknownReleaseRetainsOwnershipUntilTerminalProof(t *testing.T) {
	running, source, handler := computerRuntimeFixture(t)
	principal := computerClientPrincipal()
	opened, err := source.Open(t.Context(), principal, "desktop", "runtime-open")
	if err != nil {
		t.Fatal(err)
	}
	original := running.Computer
	handler.loseNextControl = true
	if err := closeComputerRuntime(t.Context(), running); err == nil || running.Computer != original {
		t.Fatal("ambiguous cleanup discarded original session", err)
	}
	if count := countComputerCommands(handler.commands, nodes.ComputerCommandRelease); count != 1 {
		t.Fatal("cleanup reissued an ambiguous release", count)
	}
	// Model a late remote commit. The second shutdown may query for its proof,
	// but must not send another mutation while that first outcome is unknown.
	handler.mu.Lock()
	handler.session, err = handler.session.Transition(computer.EventRelease, handler.session.Binding.Generation)
	handler.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := closeComputerRuntime(t.Context(), running); err != nil || running.Computer != nil {
		t.Fatal("terminal status did not settle original runtime", err)
	}
	if countComputerCommands(handler.commands, nodes.ComputerCommandRelease) != 1 ||
		handler.session.Binding.SessionID != opened.Binding.SessionID ||
		handler.session.Binding.WorkerID != opened.Binding.WorkerID ||
		handler.session.Binding.Target != opened.Binding.Target {
		t.Fatal("cleanup replayed or rebound the resource")
	}
}

func countComputerCommands(commands []string, command string) int {
	count := 0
	for _, value := range commands {
		if value == command {
			count++
		}
	}
	return count
}

func TestComputerReloadPreparationPinsReconciledNodeGenerationAndRollsBack(t *testing.T) {
	client, _, cfg := computerClientFixtureConfig(t, 0)
	admission := client.source.runtime
	admission.registryMu.Lock()
	admission.generation++
	admission.registryMu.Unlock()
	if _, err := client.Targets(
		t.Context(),
		computerClientPrincipal(),
		"old-generation",
	); !errors.Is(
		err,
		computer.ErrStale,
	) {
		t.Fatal("old node generation remained usable", err)
	}
	generation := &gatewayReloadGeneration{
		services: &services{NodeAdmission: admission}, cleanup: &gatewayStartupTransaction{}, computerEnabled: true,
	}
	if err := generation.prepareComputer(cfg); err != nil {
		t.Fatal(err)
	}
	if generation.services.Computer.client.source.generation != admission.invocationGeneration() {
		t.Fatal("new computer runtime pinned a retired node generation")
	}
	if _, err := generation.services.Computer.client.Targets(
		t.Context(),
		computerClientPrincipal(),
		"new-generation",
	); err != nil {
		t.Fatal("reconciled source could not discover", err)
	}
	if err := generation.cleanup.rollback(time.Second); err != nil {
		t.Fatal("prepared computer runtime rollback failed", err)
	}
	if generation.services.Computer != nil {
		t.Fatal("failed generation retained its computer runtime")
	}
}
