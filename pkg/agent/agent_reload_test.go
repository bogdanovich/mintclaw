package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestValidateConfigReloadRejectsDefaultAgentWorkspaceChange(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{{
		ID: "main", Default: true, Workspace: t.TempDir(),
	}}
	msgBus := bus.NewMessageBus()
	t.Cleanup(msgBus.Close)
	loop := NewAgentLoop(cfg, msgBus, &mockProvider{})
	t.Cleanup(loop.Close)

	next := *cfg
	next.Agents.List = append([]config.AgentConfig(nil), cfg.Agents.List...)
	next.Agents.List[0].Workspace = t.TempDir()
	err := loop.ValidateConfigReload(&next)
	if err == nil || !strings.Contains(err.Error(), "default agent workspace changes require a gateway restart") {
		t.Fatalf("ValidateConfigReload() error = %v", err)
	}
}

type preparedReloadProvider struct {
	mockProvider
	closed atomic.Int32
}

func (provider *preparedReloadProvider) Close() {
	provider.closed.Add(1)
}

func TestPrepareConfigReloadDoesNotPublishBeforeCommit(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	msgBus := bus.NewMessageBus()
	t.Cleanup(msgBus.Close)
	loop := NewAgentLoop(cfg, msgBus, &mockProvider{})
	t.Cleanup(loop.Close)
	originalRegistry := loop.GetRegistry()
	originalRunner := loop.turns.currentRunner()
	if originalRunner == nil || originalRunner != loop.turns.currentRunner() {
		t.Fatal("turn runner is not owned for the active runtime generation")
	}
	if originalRunner.runtime != loop.turns {
		t.Fatal("turn runner is detached from the loop's turn runtime")
	}
	if originalRunner.pipeline.Interaction.ToolFeedback != originalRunner.toolFeedback ||
		originalRunner.pipeline.Interaction.Suspension != originalRunner.interaction {
		t.Fatal("turn runner and pipeline do not share the generation's interaction components")
	}

	next := *cfg
	next.Agents.Defaults.ModelName = "prepared-model"
	provider := &preparedReloadProvider{}
	prepared, err := loop.PrepareConfigReload(context.Background(), provider, &next)
	if err != nil {
		t.Fatalf("PrepareConfigReload() error = %v", err)
	}
	if loop.GetRegistry() != originalRegistry || loop.GetConfig() != cfg ||
		loop.turns.currentRunner() != originalRunner {
		t.Fatal("prepare published the new registry or config")
	}

	prepared.Abort()
	if loop.GetRegistry() != originalRegistry || loop.GetConfig() != cfg ||
		loop.turns.currentRunner() != originalRunner {
		t.Fatal("abort changed the active registry or config")
	}
	if got := provider.closed.Load(); got != 1 {
		t.Fatalf("prepared provider close count = %d, want 1", got)
	}
}

func TestPrepareConfigReloadPublishesOnlyAtCommit(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	msgBus := bus.NewMessageBus()
	t.Cleanup(msgBus.Close)
	loop := NewAgentLoop(cfg, msgBus, &mockProvider{})
	t.Cleanup(loop.Close)
	originalRegistry := loop.GetRegistry()
	originalRunner := loop.turns.currentRunner()

	next := *cfg
	next.Agents.Defaults.ModelName = "committed-model"
	provider := &preparedReloadProvider{}
	prepared, err := loop.PrepareConfigReload(context.Background(), provider, &next)
	if err != nil {
		t.Fatalf("PrepareConfigReload() error = %v", err)
	}
	if err = prepared.Commit(context.Background()); err != nil {
		t.Fatalf("PreparedConfigReload.Commit() error = %v", err)
	}
	prepared.Abort()

	if loop.GetRegistry() == originalRegistry || loop.GetConfig() != &next {
		t.Fatal("commit did not publish the prepared registry and config")
	}
	committedRunner := loop.turns.currentRunner()
	if committedRunner == nil || committedRunner == originalRunner ||
		committedRunner.pipeline.Cfg != &next || originalRunner.pipeline.Cfg != cfg {
		t.Fatal("commit did not replace the turn runner generation atomically")
	}
	if committedRunner.toolFeedback == originalRunner.toolFeedback ||
		committedRunner.interaction == originalRunner.interaction ||
		committedRunner.pipeline.Interaction.Reasoning == originalRunner.pipeline.Interaction.Reasoning ||
		committedRunner.pipeline.Interaction.SyncToolDelivery == originalRunner.pipeline.Interaction.SyncToolDelivery ||
		committedRunner.pipeline.Interaction.ToolDelivery == originalRunner.pipeline.Interaction.ToolDelivery {
		t.Fatal("commit retained an interaction component from the previous runner generation")
	}
	if got := provider.closed.Load(); got != 0 {
		t.Fatalf("committed provider close count = %d, want 0", got)
	}
}

func TestPrepareConfigReloadCommitClosesPreviousProvider(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	msgBus := bus.NewMessageBus()
	t.Cleanup(msgBus.Close)
	previous := &preparedReloadProvider{}
	loop := NewAgentLoop(cfg, msgBus, previous)
	t.Cleanup(loop.Close)

	next := *cfg
	prepared, err := loop.PrepareConfigReload(context.Background(), &preparedReloadProvider{}, &next)
	if err != nil {
		t.Fatalf("PrepareConfigReload() error = %v", err)
	}
	if err = prepared.Commit(context.Background()); err != nil {
		t.Fatalf("PreparedConfigReload.Commit() error = %v", err)
	}
	if got := previous.closed.Load(); got != 1 {
		t.Fatalf("previous provider close count = %d, want 1", got)
	}
}

func TestPrepareConfigReloadRejectsStaleCommit(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	msgBus := bus.NewMessageBus()
	t.Cleanup(msgBus.Close)
	loop := NewAgentLoop(cfg, msgBus, &mockProvider{})
	t.Cleanup(loop.Close)

	firstConfig := *cfg
	first, err := loop.PrepareConfigReload(context.Background(), &preparedReloadProvider{}, &firstConfig)
	if err != nil {
		t.Fatalf("first PrepareConfigReload() error = %v", err)
	}
	defer first.Abort()
	secondConfig := *cfg
	second, err := loop.PrepareConfigReload(context.Background(), &preparedReloadProvider{}, &secondConfig)
	if err != nil {
		t.Fatalf("second PrepareConfigReload() error = %v", err)
	}
	if err = second.Commit(context.Background()); err != nil {
		t.Fatalf("second Commit() error = %v", err)
	}
	if err = first.Commit(context.Background()); err == nil {
		t.Fatal("stale Commit() error = nil, want stale registry rejection")
	}
	if loop.GetConfig() != &secondConfig {
		t.Fatal("stale commit replaced the active config")
	}
}
