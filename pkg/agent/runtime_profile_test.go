package agent

import (
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
}
