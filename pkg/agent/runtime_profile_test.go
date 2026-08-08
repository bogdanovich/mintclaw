package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestNewAgentLoopWithRuntimeProfileSeparatesExecutionAndState(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	stateRoot := filepath.Join(root, "private", "main")
	layout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-1"},
		executionRoot,
		stateRoot,
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout() error = %v", err)
	}
	profile, err := NewRuntimeProfile(RuntimeProfileBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatalf("NewRuntimeProfile() error = %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(root, "ignored-default")
	cfg.Agents.Defaults.ContextManager = "none"
	loop, err := NewAgentLoopWithRuntimeProfile(
		cfg,
		bus.NewMessageBus(),
		&mockProvider{},
		profile,
	)
	if err != nil {
		t.Fatalf("NewAgentLoopWithRuntimeProfile() error = %v", err)
	}
	t.Cleanup(loop.Close)

	agent := loop.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is nil")
	}
	if agent.Workspace != layout.ExecutionRoot() {
		t.Fatalf("Workspace = %q, want execution root %q", agent.Workspace, layout.ExecutionRoot())
	}
	if agent.Layout.StateRoot() != layout.StateRoot() {
		t.Fatalf("Layout.StateRoot() = %q, want %q", agent.Layout.StateRoot(), layout.StateRoot())
	}
	if got := agent.Tools.List(); len(got) != 0 {
		t.Fatalf("P0.2 coding tools = %v, want fail-closed empty registry before P0.4", got)
	}
	if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
		var created []string
		_ = filepath.Walk(executionRoot, func(path string, _ os.FileInfo, _ error) error {
			created = append(created, path)
			return nil
		})
		t.Fatalf("construction created execution root: stat error = %v; paths = %v", statErr, created)
	}
	if _, statErr := os.Stat(layout.StatePaths().SessionsRoot); statErr != nil {
		t.Fatalf("state sessions root was not created: %v", statErr)
	}
}

func TestNewAgentLoopWithRuntimeProfilePreflightsAllOwners(t *testing.T) {
	root := t.TempDir()
	mainExecution := filepath.Join(root, "main-project")
	mainState := filepath.Join(root, "state", "main")
	mainLayout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerPersonalAgent, ID: "main"},
		mainExecution,
		mainState,
		[]string{mainExecution},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout() error = %v", err)
	}
	profile, err := NewRuntimeProfile(RuntimeProfileBinding{AgentID: "main", Layout: mainLayout})
	if err != nil {
		t.Fatalf("NewRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	}

	loop, err := NewAgentLoopWithRuntimeProfile(
		cfg,
		bus.NewMessageBus(),
		&mockProvider{},
		profile,
		WithIsolatedToolBootstrap(),
	)
	if err == nil {
		if loop != nil {
			loop.Close()
		}
		t.Fatal("NewAgentLoopWithRuntimeProfile() error = nil, want missing-owner error")
	}
	if loop != nil {
		t.Fatalf("NewAgentLoopWithRuntimeProfile() loop = %T, want nil", loop)
	}
	if _, statErr := os.Stat(mainExecution); !os.IsNotExist(statErr) {
		t.Fatalf("failed preflight created execution root: stat error = %v", statErr)
	}
	if _, statErr := os.Stat(mainState); !os.IsNotExist(statErr) {
		t.Fatalf("failed preflight created state root: stat error = %v", statErr)
	}
}

func TestNewAgentLoopWithRuntimeProfileRejectsCodingSeahorseBeforeConstruction(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	stateRoot := filepath.Join(root, "state")
	layout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-1"},
		executionRoot,
		stateRoot,
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout() error = %v", err)
	}
	profile, err := NewRuntimeProfile(RuntimeProfileBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatalf("NewRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	if contextManagerConfigName(cfg) != "seahorse" {
		t.Fatalf("default context manager = %q, want seahorse", contextManagerConfigName(cfg))
	}

	loop, err := NewAgentLoopWithRuntimeProfile(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err == nil {
		if loop != nil {
			loop.Close()
		}
		t.Fatal("NewAgentLoopWithRuntimeProfile() error = nil, want context-manager rejection")
	}
	if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
		t.Fatalf("rejected construction created execution root: %v", statErr)
	}
	if _, statErr := os.Stat(stateRoot); !os.IsNotExist(statErr) {
		t.Fatalf("rejected construction created state root: %v", statErr)
	}
}

func TestNewAgentLoopWithRuntimeProfileDefersPersonalCutover(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "personal-project")
	stateRoot := filepath.Join(root, "personal-state")
	layout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerPersonalAgent, ID: "main"},
		executionRoot,
		stateRoot,
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout() error = %v", err)
	}
	profile, err := NewRuntimeProfile(RuntimeProfileBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatalf("NewRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"

	loop, err := NewAgentLoopWithRuntimeProfile(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err == nil {
		if loop != nil {
			loop.Close()
		}
		t.Fatal("NewAgentLoopWithRuntimeProfile() error = nil, want deferred personal cutover")
	}
	if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
		t.Fatalf("rejected personal cutover created execution root: %v", statErr)
	}
	if _, statErr := os.Stat(stateRoot); !os.IsNotExist(statErr) {
		t.Fatalf("rejected personal cutover created state root: %v", statErr)
	}
}

func TestRuntimeProfileSurvivesReload(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	stateRoot := filepath.Join(root, "state")
	layout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-reload"},
		executionRoot,
		stateRoot,
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout() error = %v", err)
	}
	profile, err := NewRuntimeProfile(RuntimeProfileBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatalf("NewRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	loop, err := NewAgentLoopWithRuntimeProfile(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err != nil {
		t.Fatalf("NewAgentLoopWithRuntimeProfile() error = %v", err)
	}
	t.Cleanup(loop.Close)

	reloaded := *cfg
	if err := loop.ReloadProviderAndConfig(context.Background(), &mockProvider{}, &reloaded); err != nil {
		t.Fatalf("ReloadProviderAndConfig() error = %v", err)
	}
	agent := loop.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("reloaded default agent is nil")
	}
	if agent.Layout.Owner() != layout.Owner() || agent.Layout.StateRoot() != layout.StateRoot() {
		t.Fatalf("reloaded layout = %#v, want owner/state from %#v", agent.Layout, layout)
	}
	if got := agent.Tools.List(); len(got) != 0 {
		t.Fatalf("reloaded coding tools = %v, want empty P0.2 registry", got)
	}
	if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
		t.Fatalf("reload created execution root: %v", statErr)
	}
}

func TestCodingRuntimeProfileKeepsMCPIsolatedAcrossReload(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	layout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-mcp"},
		executionRoot,
		filepath.Join(root, "state"),
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout() error = %v", err)
	}
	profile, err := NewRuntimeProfile(RuntimeProfileBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatalf("NewRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"must-not-start": {Enabled: true, Command: filepath.Join(root, "missing-mcp-server")},
	}
	loop, err := NewAgentLoopWithRuntimeProfile(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err != nil {
		t.Fatalf("NewAgentLoopWithRuntimeProfile() error = %v", err)
	}
	t.Cleanup(loop.Close)

	assertIsolated := func(stage string) {
		t.Helper()
		if err := loop.ensureMCPInitialized(context.Background()); err != nil {
			t.Fatalf("%s ensureMCPInitialized() error = %v", stage, err)
		}
		if loop.mcp.hasManager() {
			t.Fatalf("%s initialized MCP manager", stage)
		}
		agent := loop.GetRegistry().GetDefaultAgent()
		if got := agent.Tools.List(); len(got) != 0 {
			t.Fatalf("%s coding tools = %v, want empty registry", stage, got)
		}
	}
	assertIsolated("startup")
	reloaded := *cfg
	if err := loop.ReloadProviderAndConfig(context.Background(), &mockProvider{}, &reloaded); err != nil {
		t.Fatalf("ReloadProviderAndConfig() error = %v", err)
	}
	assertIsolated("reload")
}

func TestCodingRuntimeProfileIsolatedSkillsKeepExternalMemory(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	stateRoot := filepath.Join(root, "state")
	layout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-skills"},
		executionRoot,
		stateRoot,
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout() error = %v", err)
	}
	profile, err := NewRuntimeProfile(RuntimeProfileBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatalf("NewRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	loop, err := NewAgentLoopWithRuntimeProfile(
		cfg,
		bus.NewMessageBus(),
		&mockProvider{},
		profile,
		WithIsolatedSkillBootstrap(),
	)
	if err != nil {
		t.Fatalf("NewAgentLoopWithRuntimeProfile() error = %v", err)
	}
	t.Cleanup(loop.Close)
	if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
		t.Fatalf("isolated skill construction created execution root: %v", statErr)
	}
	if _, statErr := os.Stat(layout.StatePaths().MemoryRoot); statErr != nil {
		t.Fatalf("external memory root was not created: %v", statErr)
	}

	reloaded := *cfg
	if err := loop.ReloadProviderAndConfig(context.Background(), &mockProvider{}, &reloaded); err != nil {
		t.Fatalf("ReloadProviderAndConfig() error = %v", err)
	}
	if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
		t.Fatalf("isolated skill reload created execution root: %v", statErr)
	}
}

func TestNewRuntimeProfileValidatesBindings(t *testing.T) {
	root := t.TempDir()
	personal, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerPersonalAgent, ID: "main"},
		filepath.Join(root, "personal-project"),
		filepath.Join(root, "personal-state"),
		[]string{filepath.Join(root, "personal-project")},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout(personal) error = %v", err)
	}
	coding, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-1"},
		filepath.Join(root, "coding-project"),
		filepath.Join(root, "coding-state"),
		[]string{filepath.Join(root, "coding-project")},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout(coding) error = %v", err)
	}

	personalBinding := RuntimeProfileBinding{AgentID: "main", Layout: personal}
	if _, err = NewRuntimeProfile(personalBinding, personalBinding); err == nil {
		t.Fatal("NewRuntimeProfile(duplicate) error = nil")
	}
	if _, err = NewRuntimeProfile(RuntimeProfileBinding{AgentID: "support", Layout: personal}); err == nil {
		t.Fatal("NewRuntimeProfile(mismatched personal owner) error = nil")
	}
	if _, err = NewRuntimeProfile(RuntimeProfileBinding{AgentID: "main", Layout: coding}); err != nil {
		t.Fatalf("NewRuntimeProfile(coding) error = %v", err)
	}
	if _, err = NewRuntimeProfile(
		RuntimeProfileBinding{AgentID: "main", Layout: personal},
		RuntimeProfileBinding{AgentID: "coding", Layout: coding},
	); err == nil {
		t.Fatal("NewRuntimeProfile(mixed owners) error = nil")
	}
}

func TestRuntimeProfileRequiresExactConfiguredAgentSet(t *testing.T) {
	root := t.TempDir()
	newPersonalLayout := func(id string) RuntimeLayout {
		t.Helper()
		layout, err := NewRuntimeLayout(
			RuntimeOwner{Kind: RuntimeOwnerPersonalAgent, ID: id},
			filepath.Join(root, id+"-project"),
			filepath.Join(root, id+"-state"),
			[]string{filepath.Join(root, id+"-project")},
		)
		if err != nil {
			t.Fatalf("NewRuntimeLayout(%s) error = %v", id, err)
		}
		return layout
	}
	profile, err := NewRuntimeProfile(
		RuntimeProfileBinding{AgentID: "main", Layout: newPersonalLayout("main")},
		RuntimeProfileBinding{AgentID: "support", Layout: newPersonalLayout("support")},
	)
	if err != nil {
		t.Fatalf("NewRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	if _, err = newAgentRegistryWithRuntimeProfile(cfg, &mockProvider{}, profile); err == nil {
		t.Fatal("newAgentRegistryWithRuntimeProfile(extra binding) error = nil")
	}
	cfg.Agents.List = []config.AgentConfig{{ID: "main"}, {ID: " MAIN "}}
	if _, err = newAgentRegistryWithRuntimeProfile(cfg, &mockProvider{}, profile); err == nil {
		t.Fatal("newAgentRegistryWithRuntimeProfile(duplicate configured ID) error = nil")
	}
}
