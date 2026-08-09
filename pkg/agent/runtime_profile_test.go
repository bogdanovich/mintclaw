package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/seahorse"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

var errInjectedRuntimeStore = errors.New("injected runtime store failure")

type trackedRuntimeSessionStore struct {
	session.SessionStore
	closeCount int
}

func (s *trackedRuntimeSessionStore) Close() error {
	s.closeCount++
	return s.SessionStore.Close()
}

type trackingRuntimeStoreFactory struct {
	delegate       defaultRuntimeStoreFactory
	failSessionAt  int
	failSeahorse   bool
	failSeahorseAt int
	sessionCalls   int
	seahorseCalls  int
	sessions       []*trackedRuntimeSessionStore
	seahorsePaths  []string
	engines        []*seahorse.Engine
}

func (f *trackingRuntimeStoreFactory) NewSessionStore(layout RuntimeLayout) (session.SessionStore, error) {
	f.sessionCalls++
	if f.sessionCalls == f.failSessionAt {
		return nil, errInjectedRuntimeStore
	}
	store, err := f.delegate.NewSessionStore(layout)
	if err != nil {
		return nil, err
	}
	tracked := &trackedRuntimeSessionStore{SessionStore: store}
	f.sessions = append(f.sessions, tracked)
	return tracked, nil
}

func (f *trackingRuntimeStoreFactory) NewSeahorseEngine(
	config seahorse.Config,
	complete seahorse.CompleteFn,
) (*seahorse.Engine, error) {
	f.seahorseCalls++
	f.seahorsePaths = append(f.seahorsePaths, config.DBPath)
	if f.failSeahorse || f.seahorseCalls == f.failSeahorseAt {
		return nil, errInjectedRuntimeStore
	}
	engine, err := f.delegate.NewSeahorseEngine(config, complete)
	if err == nil {
		f.engines = append(f.engines, engine)
	}
	return engine, err
}

type countingStatefulProvider struct {
	closeCount int
}

func (p *countingStatefulProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{}, nil
}

func (p *countingStatefulProvider) GetDefaultModel() string { return "test" }

func (p *countingStatefulProvider) Close() { p.closeCount++ }

func TestAgentInstanceCloseOwnsOnlyInternallyCreatedProviders(t *testing.T) {
	injected := &countingStatefulProvider{}
	created := &countingStatefulProvider{}
	ownership := newProviderOwnership(injected)
	ownership.trackCreated(injected)
	ownership.trackCreated(created)
	ownership.trackCreated(created)
	agent := &AgentInstance{ownedProviders: ownership.owned}

	if err := agent.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := agent.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if injected.closeCount != 0 {
		t.Fatalf("injected provider close count = %d, want 0", injected.closeCount)
	}
	if created.closeCount != 1 {
		t.Fatalf("internally created provider close count = %d, want 1", created.closeCount)
	}
}

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
	identity := agent.ContextBuilder.getIdentity(true)
	externalMemoryFile := filepath.Join(layout.StatePaths().MemoryRoot, "MEMORY.md")
	if !strings.Contains(identity, externalMemoryFile) {
		t.Fatalf("runtime identity does not advertise external memory file %q:\n%s", externalMemoryFile, identity)
	}
	if strings.Contains(identity, filepath.Join(executionRoot, "memory")) {
		t.Fatalf("runtime identity advertises execution-root memory:\n%s", identity)
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

func TestNewRuntimeProfileRejectsStateInsideAnotherExecutionRoot(t *testing.T) {
	root := t.TempDir()
	firstExecution := filepath.Join(root, "project-a")
	secondExecution := filepath.Join(root, "project-b")
	firstLayout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-a"},
		firstExecution,
		filepath.Join(secondExecution, ".mintclaw", "thread-a"),
		[]string{firstExecution},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout(first) error = %v", err)
	}
	secondState := filepath.Join(root, "state", "thread-b")
	secondLayout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-b"},
		secondExecution,
		secondState,
		[]string{secondExecution},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout(second) error = %v", err)
	}

	profile, err := NewRuntimeProfile(
		RuntimeProfileBinding{AgentID: "main", Layout: firstLayout},
		RuntimeProfileBinding{AgentID: "support", Layout: secondLayout},
	)
	if err == nil {
		t.Fatalf("NewRuntimeProfile() = %#v, want cross-agent root rejection", profile)
	}
	if _, statErr := os.Stat(firstLayout.StateRoot()); !os.IsNotExist(statErr) {
		t.Fatalf("rejected profile created first state root: %v", statErr)
	}
	if _, statErr := os.Stat(secondState); !os.IsNotExist(statErr) {
		t.Fatalf("rejected profile created second state root: %v", statErr)
	}
}

func TestNewRuntimeProfileRejectsOverlappingStateRootsForDistinctOwners(t *testing.T) {
	for _, test := range []struct {
		name        string
		secondState func(string) string
	}{
		{
			name: "shared root",
			secondState: func(firstState string) string {
				return firstState
			},
		},
		{
			name: "nested root",
			secondState: func(firstState string) string {
				return filepath.Join(firstState, "support")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			firstState := filepath.Join(root, "state", "thread-a")
			firstLayout, err := NewRuntimeLayout(
				RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-a"},
				filepath.Join(root, "project-a"),
				firstState,
				[]string{filepath.Join(root, "project-a")},
			)
			if err != nil {
				t.Fatalf("NewRuntimeLayout(first) error = %v", err)
			}
			secondLayout, err := NewRuntimeLayout(
				RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-b"},
				filepath.Join(root, "project-b"),
				test.secondState(firstState),
				[]string{filepath.Join(root, "project-b")},
			)
			if err != nil {
				t.Fatalf("NewRuntimeLayout(second) error = %v", err)
			}

			profile, err := NewRuntimeProfile(
				RuntimeProfileBinding{AgentID: "main", Layout: firstLayout},
				RuntimeProfileBinding{AgentID: "support", Layout: secondLayout},
			)
			if err == nil {
				t.Fatalf("NewRuntimeProfile() = %#v, want overlapping-state rejection", profile)
			}
			if _, statErr := os.Stat(firstState); !os.IsNotExist(statErr) {
				t.Fatalf("rejected profile created state root: %v", statErr)
			}
		})
	}
}

func TestNewAgentLoopWithRuntimeProfileRejectsUnusableStatePaths(t *testing.T) {
	for _, test := range []struct {
		name      string
		blockPath func(RuntimeLayout) string
	}{
		{
			name: "sessions root",
			blockPath: func(layout RuntimeLayout) string {
				return layout.StatePaths().SessionsRoot
			},
		},
		{
			name: "context root",
			blockPath: func(layout RuntimeLayout) string {
				return layout.StatePaths().ContextRoot
			},
		},
		{
			name: "memory root",
			blockPath: func(layout RuntimeLayout) string {
				return layout.StatePaths().MemoryRoot
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			executionRoot := filepath.Join(root, "project")
			layout, err := NewRuntimeLayout(
				RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-state-error"},
				executionRoot,
				filepath.Join(root, "state"),
				[]string{executionRoot},
			)
			if err != nil {
				t.Fatalf("NewRuntimeLayout() error = %v", err)
			}
			blockedPath := test.blockPath(layout)
			if err := os.MkdirAll(filepath.Dir(blockedPath), 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if err := os.WriteFile(blockedPath, []byte("not a directory"), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
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
				t.Fatal("NewAgentLoopWithRuntimeProfile() error = nil, want unusable-state error")
			}
			if loop != nil {
				t.Fatalf("NewAgentLoopWithRuntimeProfile() loop = %T, want nil", loop)
			}
			if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
				t.Fatalf("failed construction created execution root: %v", statErr)
			}
		})
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

func TestNewAgentLoopWithRuntimeProfilePreflightsLaterStateBeforeConstruction(t *testing.T) {
	root := t.TempDir()
	mainExecution := filepath.Join(root, "main-project")
	mainState := filepath.Join(root, "state", "main")
	mainLayout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-main"},
		mainExecution,
		mainState,
		[]string{mainExecution},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout(main) error = %v", err)
	}
	supportExecution := filepath.Join(root, "support-project")
	supportState := filepath.Join(root, "state", "support")
	supportLayout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-support"},
		supportExecution,
		supportState,
		[]string{supportExecution},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout(support) error = %v", err)
	}
	if err := os.MkdirAll(supportState, 0o755); err != nil {
		t.Fatalf("MkdirAll(support state) error = %v", err)
	}
	if err := os.WriteFile(supportLayout.StatePaths().SessionsRoot, []byte("blocked"), 0o600); err != nil {
		t.Fatalf("WriteFile(blocked sessions) error = %v", err)
	}
	profile, err := NewRuntimeProfile(
		RuntimeProfileBinding{AgentID: "main", Layout: mainLayout},
		RuntimeProfileBinding{AgentID: "support", Layout: supportLayout},
	)
	if err != nil {
		t.Fatalf("NewRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	}

	loop, err := NewAgentLoopWithRuntimeProfile(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err == nil {
		if loop != nil {
			loop.Close()
		}
		t.Fatal("NewAgentLoopWithRuntimeProfile() error = nil, want later-state preflight error")
	}
	if loop != nil {
		t.Fatalf("NewAgentLoopWithRuntimeProfile() loop = %T, want nil", loop)
	}
	if _, statErr := os.Stat(mainState); !os.IsNotExist(statErr) {
		t.Fatalf("failed preflight created earlier owner state: %v", statErr)
	}
	if _, statErr := os.Stat(mainExecution); !os.IsNotExist(statErr) {
		t.Fatalf("failed preflight created earlier execution root: %v", statErr)
	}
}

func TestNewAgentLoopWithRuntimeProfileRevalidatesPhysicalStateIsolation(t *testing.T) {
	root := t.TempDir()
	mainExecution := filepath.Join(root, "main-project")
	mainState := filepath.Join(root, "state-main")
	mainLayout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-main"},
		mainExecution,
		mainState,
		[]string{mainExecution},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout(main) error = %v", err)
	}
	supportExecution := filepath.Join(root, "support-project")
	supportState := filepath.Join(root, "state-support")
	supportLayout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-support"},
		supportExecution,
		supportState,
		[]string{supportExecution},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout(support) error = %v", err)
	}
	profile, err := NewRuntimeProfile(
		RuntimeProfileBinding{AgentID: "main", Layout: mainLayout},
		RuntimeProfileBinding{AgentID: "support", Layout: supportLayout},
	)
	if err != nil {
		t.Fatalf("NewRuntimeProfile() error = %v", err)
	}
	if err := os.MkdirAll(mainState, 0o755); err != nil {
		t.Fatalf("MkdirAll(main state) error = %v", err)
	}
	if err := os.Symlink(mainState, supportState); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	}

	loop, err := NewAgentLoopWithRuntimeProfile(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err == nil {
		if loop != nil {
			loop.Close()
		}
		t.Fatal("NewAgentLoopWithRuntimeProfile() error = nil, want physical-root isolation error")
	}
	if loop != nil {
		t.Fatalf("NewAgentLoopWithRuntimeProfile() loop = %T, want nil", loop)
	}
	for _, path := range []string{
		mainLayout.StatePaths().SessionsRoot,
		mainLayout.StatePaths().MemoryRoot,
	} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("failed physical-root preflight created %q: %v", path, statErr)
		}
	}
}

func TestNewAgentLoopWithRuntimeProfileRejectsDerivedStateSymlinks(t *testing.T) {
	for _, test := range []struct {
		name       string
		linkedPath func(RuntimeLayout) string
	}{
		{
			name: "sessions",
			linkedPath: func(layout RuntimeLayout) string {
				return layout.StatePaths().SessionsRoot
			},
		},
		{
			name: "context",
			linkedPath: func(layout RuntimeLayout) string {
				return layout.StatePaths().ContextRoot
			},
		},
		{
			name: "memory",
			linkedPath: func(layout RuntimeLayout) string {
				return layout.StatePaths().MemoryRoot
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			executionRoot := filepath.Join(root, "project")
			stateRoot := filepath.Join(root, "state")
			layout, err := NewRuntimeLayout(
				RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-symlink"},
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
			target := filepath.Join(executionRoot, "redirected-state")
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatalf("MkdirAll(target) error = %v", err)
			}
			if err := os.MkdirAll(stateRoot, 0o755); err != nil {
				t.Fatalf("MkdirAll(state root) error = %v", err)
			}
			if err := os.Symlink(target, test.linkedPath(layout)); err != nil {
				t.Skipf("Symlink() unavailable: %v", err)
			}
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.ContextManager = "none"

			loop, err := NewAgentLoopWithRuntimeProfile(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
			if err == nil {
				if loop != nil {
					loop.Close()
				}
				t.Fatal("NewAgentLoopWithRuntimeProfile() error = nil, want derived-symlink rejection")
			}
			if loop != nil {
				t.Fatalf("NewAgentLoopWithRuntimeProfile() loop = %T, want nil", loop)
			}
			entries, readErr := os.ReadDir(target)
			if readErr != nil {
				t.Fatalf("ReadDir(redirected target) error = %v", readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("failed preflight wrote redirected source state: %v", entries)
			}
		})
	}
}

func TestNewAgentLoopWithRuntimeProfileRejectsSeahorseDatabaseSymlink(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	layout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-db-symlink"},
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
	if err := os.MkdirAll(layout.StatePaths().ContextRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(context root) error = %v", err)
	}
	target := filepath.Join(root, "outside.db")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	dbPath := filepath.Join(layout.StatePaths().ContextRoot, "seahorse.db")
	if err := os.Symlink(target, dbPath); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}
	cfg := config.DefaultConfig()

	loop, err := NewAgentLoopWithRuntimeProfile(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err == nil {
		if loop != nil {
			loop.Close()
		}
		t.Fatal("NewAgentLoopWithRuntimeProfile() error = nil, want Seahorse symlink rejection")
	}
	if loop != nil {
		t.Fatalf("NewAgentLoopWithRuntimeProfile() loop = %T, want nil", loop)
	}
	info, statErr := os.Stat(target)
	if statErr != nil || info.Size() != 0 {
		t.Fatalf("rejected construction mutated symlink target: info=%v error=%v", info, statErr)
	}
}

func TestNewAgentLoopWithRuntimeProfileChecksLaterStateCreatability(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not enforce Unix directory mode bits")
	}
	root := t.TempDir()
	mainExecution := filepath.Join(root, "main-project")
	mainState := filepath.Join(root, "state-main")
	mainLayout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-main"},
		mainExecution,
		mainState,
		[]string{mainExecution},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout(main) error = %v", err)
	}
	supportExecution := filepath.Join(root, "support-project")
	supportState := filepath.Join(root, "state-support")
	supportLayout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-support"},
		supportExecution,
		supportState,
		[]string{supportExecution},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout(support) error = %v", err)
	}
	if err := os.MkdirAll(supportState, 0o500); err != nil {
		t.Fatalf("MkdirAll(support state) error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(supportState, 0o700)
	})
	profile, err := NewRuntimeProfile(
		RuntimeProfileBinding{AgentID: "main", Layout: mainLayout},
		RuntimeProfileBinding{AgentID: "support", Layout: supportLayout},
	)
	if err != nil {
		t.Fatalf("NewRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	}

	loop, err := NewAgentLoopWithRuntimeProfile(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err == nil {
		if loop != nil {
			loop.Close()
		}
		t.Fatal("NewAgentLoopWithRuntimeProfile() error = nil, want state-creatability error")
	}
	if loop != nil {
		t.Fatalf("NewAgentLoopWithRuntimeProfile() loop = %T, want nil", loop)
	}
	if _, statErr := os.Stat(mainState); !os.IsNotExist(statErr) {
		t.Fatalf("failed creatability preflight created earlier owner state: %v", statErr)
	}
}

func TestNewAgentLoopWithRuntimeProfileDoesNotMigrateLegacySessions(t *testing.T) {
	root := t.TempDir()
	mainExecution := filepath.Join(root, "main-project")
	mainLayout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-main"},
		mainExecution,
		filepath.Join(root, "state-main"),
		[]string{mainExecution},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout(main) error = %v", err)
	}
	supportExecution := filepath.Join(root, "support-project")
	supportLayout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-support"},
		supportExecution,
		filepath.Join(root, "state-support"),
		[]string{supportExecution},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout(support) error = %v", err)
	}
	supportSessions := supportLayout.StatePaths().SessionsRoot
	if err := os.MkdirAll(filepath.Join(supportSessions, "blocked.meta.json"), 0o755); err != nil {
		t.Fatalf("MkdirAll(blocked metadata) error = %v", err)
	}
	legacySession := filepath.Join(supportSessions, "blocked.json")
	if err := os.WriteFile(legacySession, []byte(`{"key":"blocked","messages":[]}`), 0o600); err != nil {
		t.Fatalf("WriteFile(legacy session) error = %v", err)
	}
	profile, err := NewRuntimeProfile(
		RuntimeProfileBinding{AgentID: "main", Layout: mainLayout},
		RuntimeProfileBinding{AgentID: "support", Layout: supportLayout},
	)
	if err != nil {
		t.Fatalf("NewRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	}

	loop, err := NewAgentLoopWithRuntimeProfile(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err != nil {
		t.Fatalf("NewAgentLoopWithRuntimeProfile() error = %v", err)
	}
	t.Cleanup(loop.Close)
	if _, statErr := os.Stat(legacySession); statErr != nil {
		t.Fatalf("strict runtime mutated legacy session: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(supportSessions, "blocked.jsonl")); !os.IsNotExist(statErr) {
		t.Fatalf("strict runtime created migrated JSONL: %v", statErr)
	}
}

func TestNewAgentLoopWithRuntimeProfileRoutesCodingSeahorseToStateRoot(t *testing.T) {
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
	if err != nil {
		t.Fatalf("NewAgentLoopWithRuntimeProfile() error = %v", err)
	}
	t.Cleanup(loop.Close)
	if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
		t.Fatalf("strict construction created execution root: %v", statErr)
	}
	wantDB := filepath.Join(layout.StatePaths().ContextRoot, "seahorse.db")
	if _, statErr := os.Stat(wantDB); statErr != nil {
		t.Fatalf("Seahorse DB under state root: %v", statErr)
	}
	legacyDB := filepath.Join(executionRoot, "sessions", "seahorse.db")
	if _, statErr := os.Stat(legacyDB); !os.IsNotExist(statErr) {
		t.Fatalf("unexpected execution-root Seahorse DB: %v", statErr)
	}
}

func TestRuntimeProfileSeparatesSeahorseDatabasesByCodingThread(t *testing.T) {
	root := t.TempDir()
	bindings := make([]RuntimeProfileBinding, 0, 2)
	wantPaths := make([]string, 0, 2)
	for _, spec := range []struct {
		agentID string
		thread  string
	}{
		{agentID: "main", thread: "thread-one"},
		{agentID: "support", thread: "thread-two"},
	} {
		executionRoot := filepath.Join(root, "projects", spec.thread)
		layout, err := NewRuntimeLayout(
			RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: spec.thread},
			executionRoot,
			filepath.Join(root, "state", spec.thread),
			[]string{executionRoot},
		)
		if err != nil {
			t.Fatalf("NewRuntimeLayout(%s) error = %v", spec.thread, err)
		}
		bindings = append(bindings, RuntimeProfileBinding{AgentID: spec.agentID, Layout: layout})
		wantPaths = append(wantPaths, filepath.Join(layout.StatePaths().ContextRoot, "seahorse.db"))
	}
	factory := &trackingRuntimeStoreFactory{}
	profile, err := NewRuntimeProfileWithStoreFactory(factory, bindings...)
	if err != nil {
		t.Fatalf("NewRuntimeProfileWithStoreFactory() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	}

	loop, err := NewAgentLoopWithRuntimeProfile(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err != nil {
		t.Fatalf("NewAgentLoopWithRuntimeProfile() error = %v", err)
	}
	t.Cleanup(loop.Close)
	if len(factory.seahorsePaths) != len(wantPaths) {
		t.Fatalf("Seahorse paths = %v, want %v", factory.seahorsePaths, wantPaths)
	}
	gotPaths := make(map[string]struct{}, len(factory.seahorsePaths))
	for _, gotPath := range factory.seahorsePaths {
		gotPaths[gotPath] = struct{}{}
	}
	for index, wantPath := range wantPaths {
		if _, ok := gotPaths[wantPath]; !ok {
			t.Fatalf("Seahorse paths = %v, missing %q", factory.seahorsePaths, wantPath)
		}
		if _, statErr := os.Stat(wantPath); statErr != nil {
			t.Fatalf("Seahorse DB %d: %v", index, statErr)
		}
	}
	if wantPaths[0] == wantPaths[1] {
		t.Fatalf("distinct coding threads share Seahorse DB %q", wantPaths[0])
	}
}

func TestRuntimeProfileStoreConstructionRollsBackEarlierSessions(t *testing.T) {
	root := t.TempDir()
	bindings := make([]RuntimeProfileBinding, 0, 2)
	for _, id := range []string{"main", "support"} {
		executionRoot := filepath.Join(root, "projects", id)
		layout, err := NewRuntimeLayout(
			RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-" + id},
			executionRoot,
			filepath.Join(root, "state", id),
			[]string{executionRoot},
		)
		if err != nil {
			t.Fatalf("NewRuntimeLayout(%s) error = %v", id, err)
		}
		bindings = append(bindings, RuntimeProfileBinding{AgentID: id, Layout: layout})
	}
	factory := &trackingRuntimeStoreFactory{failSessionAt: 2}
	profile, err := NewRuntimeProfileWithStoreFactory(factory, bindings...)
	if err != nil {
		t.Fatalf("NewRuntimeProfileWithStoreFactory() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	}

	loop, err := NewAgentLoopWithRuntimeProfile(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if !errors.Is(err, errInjectedRuntimeStore) {
		if loop != nil {
			loop.Close()
		}
		t.Fatalf("NewAgentLoopWithRuntimeProfile() error = %v, want injected failure", err)
	}
	if loop != nil {
		t.Fatalf("NewAgentLoopWithRuntimeProfile() loop = %T, want nil", loop)
	}
	if len(factory.sessions) != 1 || factory.sessions[0].closeCount != 1 {
		t.Fatalf("opened sessions = %#v, want one store closed exactly once", factory.sessions)
	}
}

func TestRuntimeProfileContextConstructionFailureClosesCanonicalStore(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	layout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-context-failure"},
		executionRoot,
		filepath.Join(root, "state"),
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout() error = %v", err)
	}
	factory := &trackingRuntimeStoreFactory{failSeahorse: true}
	profile, err := NewRuntimeProfileWithStoreFactory(
		factory,
		RuntimeProfileBinding{AgentID: "main", Layout: layout},
	)
	if err != nil {
		t.Fatalf("NewRuntimeProfileWithStoreFactory() error = %v", err)
	}
	cfg := config.DefaultConfig()

	loop, err := NewAgentLoopWithRuntimeProfile(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if !errors.Is(err, errInjectedRuntimeStore) {
		if loop != nil {
			loop.Close()
		}
		t.Fatalf("NewAgentLoopWithRuntimeProfile() error = %v, want injected failure", err)
	}
	if loop != nil {
		t.Fatalf("NewAgentLoopWithRuntimeProfile() loop = %T, want nil", loop)
	}
	if len(factory.sessions) != 1 || factory.sessions[0].closeCount != 1 {
		t.Fatalf("opened sessions = %#v, want canonical store closed exactly once", factory.sessions)
	}
	wantDB := filepath.Join(layout.StatePaths().ContextRoot, "seahorse.db")
	if len(factory.seahorsePaths) != 1 || factory.seahorsePaths[0] != wantDB {
		t.Fatalf("Seahorse paths = %v, want [%q]", factory.seahorsePaths, wantDB)
	}
}

func TestRuntimeProfileLaterContextFailureClosesPartialManager(t *testing.T) {
	root := t.TempDir()
	bindings := make([]RuntimeProfileBinding, 0, 2)
	for _, id := range []string{"main", "support"} {
		executionRoot := filepath.Join(root, "projects", id)
		layout, err := NewRuntimeLayout(
			RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-context-" + id},
			executionRoot,
			filepath.Join(root, "state", id),
			[]string{executionRoot},
		)
		if err != nil {
			t.Fatalf("NewRuntimeLayout(%s) error = %v", id, err)
		}
		bindings = append(bindings, RuntimeProfileBinding{AgentID: id, Layout: layout})
	}
	factory := &trackingRuntimeStoreFactory{failSeahorseAt: 2}
	profile, err := NewRuntimeProfileWithStoreFactory(factory, bindings...)
	if err != nil {
		t.Fatalf("NewRuntimeProfileWithStoreFactory() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	}

	loop, err := NewAgentLoopWithRuntimeProfile(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if !errors.Is(err, errInjectedRuntimeStore) {
		if loop != nil {
			loop.Close()
		}
		t.Fatalf("NewAgentLoopWithRuntimeProfile() error = %v, want injected failure", err)
	}
	if loop != nil {
		t.Fatalf("NewAgentLoopWithRuntimeProfile() loop = %T, want nil", loop)
	}
	if len(factory.sessions) != 2 {
		t.Fatalf("opened sessions = %d, want 2", len(factory.sessions))
	}
	for index, store := range factory.sessions {
		if store.closeCount != 1 {
			t.Fatalf("session store %d close count = %d, want 1", index, store.closeCount)
		}
	}
	if len(factory.engines) != 1 {
		t.Fatalf("opened Seahorse engines = %d, want 1", len(factory.engines))
	}
	if _, assembleErr := factory.engines[0].Assemble(
		t.Context(),
		"closed-after-rollback",
		seahorse.AssembleInput{Budget: 100},
	); assembleErr == nil {
		t.Fatal("partial Seahorse manager left its first engine open")
	}
}

func TestRuntimeProfileRejectsCustomSeahorsePathAndRollsBack(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	layout, err := NewRuntimeLayout(
		RuntimeOwner{Kind: RuntimeOwnerCodingThread, ID: "thread-custom-db"},
		executionRoot,
		filepath.Join(root, "state"),
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewRuntimeLayout() error = %v", err)
	}
	factory := &trackingRuntimeStoreFactory{}
	profile, err := NewRuntimeProfileWithStoreFactory(
		factory,
		RuntimeProfileBinding{AgentID: "main", Layout: layout},
	)
	if err != nil {
		t.Fatalf("NewRuntimeProfileWithStoreFactory() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManagerConfig = []byte(`{"dbPath":"/tmp/not-owner-scoped.db"}`)

	loop, err := NewAgentLoopWithRuntimeProfile(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err == nil || !strings.Contains(err.Error(), "custom dbPath") {
		if loop != nil {
			loop.Close()
		}
		t.Fatalf("NewAgentLoopWithRuntimeProfile() error = %v, want custom-dbPath rejection", err)
	}
	if len(factory.sessions) != 1 || factory.sessions[0].closeCount != 1 {
		t.Fatalf("opened sessions = %#v, want canonical store closed exactly once", factory.sessions)
	}
	if len(factory.seahorsePaths) != 0 {
		t.Fatalf("Seahorse factory called with rejected path: %v", factory.seahorsePaths)
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
		t.Fatal("NewAgentLoopWithRuntimeProfile() error = nil, want P0.4 personal-tool cutover guard")
	}
	if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
		t.Fatalf("rejected personal cutover created execution root: %v", statErr)
	}
	if _, statErr := os.Stat(stateRoot); !os.IsNotExist(statErr) {
		t.Fatalf("rejected personal cutover created state root: %v", statErr)
	}
}

func TestRuntimeProfileRejectsHotReloadWithoutMutation(t *testing.T) {
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
	originalRegistry := loop.GetRegistry()

	reloaded := *cfg
	if err := loop.ReloadProviderAndConfig(context.Background(), &mockProvider{}, &reloaded); err == nil {
		t.Fatal("ReloadProviderAndConfig() error = nil, want restart requirement")
	}
	if loop.GetRegistry() != originalRegistry {
		t.Fatal("rejected reload replaced the runtime-profile registry")
	}
	agent := loop.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is nil after rejected reload")
	}
	if agent.Layout.Owner() != layout.Owner() || agent.Layout.StateRoot() != layout.StateRoot() {
		t.Fatalf("layout after rejected reload = %#v, want %#v", agent.Layout, layout)
	}
	if got := agent.Tools.List(); len(got) != 0 {
		t.Fatalf("coding tools after rejected reload = %v, want empty P0.2 registry", got)
	}
	if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
		t.Fatalf("rejected reload created execution root: %v", statErr)
	}
}

func TestCodingRuntimeProfileKeepsMCPIsolatedWhenReloadRejected(t *testing.T) {
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
	if err := loop.ReloadProviderAndConfig(context.Background(), &mockProvider{}, &reloaded); err == nil {
		t.Fatal("ReloadProviderAndConfig() error = nil, want restart requirement")
	}
	assertIsolated("rejected reload")
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
	if err := loop.ReloadProviderAndConfig(context.Background(), &mockProvider{}, &reloaded); err == nil {
		t.Fatal("ReloadProviderAndConfig() error = nil, want restart requirement")
	}
	if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
		t.Fatalf("rejected isolated-skill reload created execution root: %v", statErr)
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
