package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

func TestAgentLoopCloseContextBoundsInteractionObservers(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &simpleConvProvider{})
	registry := fixture.Loop.interactionRegistryForWorkspace(fixture.Agent.Workspace)
	if registry == nil {
		t.Fatal("registry missing")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	registry.Subscribe(func(interactions.EventObservation) { close(entered); <-release })
	done := make(chan error, 1)
	go func() {
		_, err := registry.Create(interactionShutdownRequest(fixture.Agent.ID, "interaction_runtimeclose"))
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("observer did not start")
	}
	started := time.Now()
	err := fixture.Loop.CloseContext(context.Background())
	elapsed := time.Since(started)
	if !errors.Is(err, interactions.ErrObserverPending) || !errors.Is(err, context.DeadlineExceeded) ||
		elapsed > 2*time.Second {
		t.Fatalf("observer shutdown was not bounded: elapsed=%s err=%v", elapsed, err)
	}
	if fixture.Agent.Sessions == nil {
		t.Fatal("shutdown closed dependencies while callback still ran")
	}
	if got := fixture.Loop.interactions.registryForWorkspace(t.TempDir()); got != nil {
		t.Fatal("shutdown admitted a new interaction registry")
	}
	select {
	case <-done:
		t.Fatal("shutdown falsely canceled the executing callback")
	default:
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not settle")
	}
	if err := fixture.Loop.CloseContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Logf("runtime observer shutdown: elapsed=%s pending_explicit=true retry_settled=true", elapsed)
}

func interactionShutdownRequest(agentID, id string) interactions.CreateRequest {
	return interactions.CreateRequest{
		ID:   id,
		Kind: interactions.KindQuestion,
		Route: interactions.Route{
			AgentID:    agentID,
			SessionKey: id,
			Channel:    "telegram",
			ChatID:     "chat-1",
			SenderID:   "user-1",
		},
		Origin: interactions.Origin{
			TurnID:     "shutdown-turn",
			ToolCallID: "shutdown-call",
			ToolName:   "request_user_input",
		},
		Questions: []interactions.Question{
			{
				ID:       "target",
				Header:   "Target",
				Question: "Which target?",
				Options:  []interactions.Option{{Label: "Staging"}, {Label: "Production"}},
			},
		},
		PromptSummary: "Choose a target.",
		ExpiresAt:     time.Now().Add(time.Hour),
	}
}

func TestInteractionCoordinatorShutdownSharesWorkspaceBudget(t *testing.T) {
	coordinator := newInteractionCoordinator(t.TempDir())
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	done := make(chan error, 2)
	for i := range 2 {
		registry := interactions.NewRegistry("")
		entered := make(chan struct{})
		registry.Subscribe(func(interactions.EventObservation) { close(entered); <-release })
		coordinator.registries.Store(fmt.Sprintf("workspace-%d", i), registry)
		go func() {
			_, err := registry.Create(interactionShutdownRequest("main", fmt.Sprintf("interaction_%dclosing", i)))
			done <- err
		}()
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("workspace observer did not start")
		}
	}
	started := time.Now()
	err := coordinator.closeRegistries(t.Context())
	elapsed := time.Since(started)
	if !errors.Is(err, interactions.ErrObserverPending) || elapsed > 1800*time.Millisecond {
		t.Fatalf("workspace deadlines accumulated: elapsed=%s err=%v", elapsed, err)
	}
	unblock()
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("observer did not settle")
		}
	}
	if err := coordinator.closeRegistries(t.Context()); err != nil {
		t.Fatal(err)
	}
}
