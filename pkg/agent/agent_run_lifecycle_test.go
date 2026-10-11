package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type runLifecycleBlockingProvider struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
}

func newRunLifecycleBlockingProvider() *runLifecycleBlockingProvider {
	return &runLifecycleBlockingProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (p *runLifecycleBlockingProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	p.startedOnce.Do(func() { close(p.started) })
	<-p.release
	return &providers.LLMResponse{Content: "done"}, nil
}

func (*runLifecycleBlockingProvider) GetDefaultModel() string { return "test-model" }

func (p *runLifecycleBlockingProvider) unblock() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func TestAgentLoopCloseContextWaitsForInboundWorkerDrain(t *testing.T) {
	al, msgBus, provider, runDone := startRunLifecycleTestLoop(t)

	closeCtx, cancelClose := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelClose()
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- al.CloseContext(closeCtx)
	}()

	select {
	case err := <-closeDone:
		t.Fatalf("CloseContext() returned before worker drain: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if agent := al.GetRegistry().GetDefaultAgent(); agent == nil || agent.Sessions == nil {
		t.Fatal("CloseContext() closed registry-owned sessions before worker drain")
	}

	provider.unblock()
	if err := <-closeDone; err != nil {
		t.Fatalf("CloseContext() error = %v", err)
	}
	if err := <-runDone; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	msgBus.Close()
}

func TestAgentLoopCloseContextTimeoutLeavesRegistryOpenForRetry(t *testing.T) {
	al, msgBus, provider, runDone := startRunLifecycleTestLoop(t)

	closeCtx, cancelClose := context.WithTimeout(t.Context(), 25*time.Millisecond)
	err := al.CloseContext(closeCtx)
	cancelClose()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext() error = %v, want context deadline exceeded", err)
	}
	if agent := al.GetRegistry().GetDefaultAgent(); agent == nil || agent.Sessions == nil {
		t.Fatal("timed-out CloseContext() closed registry-owned sessions")
	}

	provider.unblock()
	retryCtx, cancelRetry := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelRetry()
	if err := al.CloseContext(retryCtx); err != nil {
		t.Fatalf("CloseContext() retry error = %v", err)
	}
	if err := <-runDone; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	msgBus.Close()
}

func startRunLifecycleTestLoop(
	t *testing.T,
) (*AgentLoop, *bus.MessageBus, *runLifecycleBlockingProvider, <-chan error) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.Defaults.ContextManagerConfig = nil
	cfg.Agents.Defaults.ModelName = "test-model"

	msgBus := bus.NewMessageBus()
	provider := newRunLifecycleBlockingProvider()
	t.Cleanup(provider.unblock)
	t.Cleanup(msgBus.Close)
	al := NewAgentLoop(cfg, msgBus, provider)
	runDone := make(chan error, 1)
	go func() {
		runDone <- al.Run(t.Context())
	}()
	if err := al.WaitStartup(t.Context()); err != nil {
		t.Fatalf("WaitStartup() error = %v", err)
	}
	if err := msgBus.PublishInbound(t.Context(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
		},
		Content: "block until close starts",
	}); err != nil {
		t.Fatalf("PublishInbound() error = %v", err)
	}
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	return al, msgBus, provider, runDone
}
