package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/seahorse"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

var errInjectedRuntimeStore = errors.New("injected runtime store failure")

var codingRuntimeToolNames = []string{
	"append_file",
	"apply_patch",
	"exec",
	"list_dir",
	"read_file",
	"search_files",
	"update_plan",
	"write_file",
}

type trackedRuntimeSessionStore struct {
	session.SessionStore
	closeCount int
}

type failingRuntimeCloser struct {
	err        error
	closeCount int
}

func (c *failingRuntimeCloser) Close() error {
	c.closeCount++
	return c.err
}

type failingRuntimeSessionStore struct {
	session.SessionStore
	err        error
	closeCount int
}

func (s *failingRuntimeSessionStore) Close() error {
	s.closeCount++
	return s.err
}

func (s *trackedRuntimeSessionStore) Close() error {
	s.closeCount++
	return s.SessionStore.Close()
}

type trackingCodingRuntimeStoreFactory struct {
	delegate        defaultCodingRuntimeStoreFactory
	failSessionAt   int
	failSeahorse    bool
	failSeahorseAt  int
	nilSession      bool
	typedNilSession bool
	nilSeahorse     bool
	sessionCalls    int
	seahorseCalls   int
	sessions        []*trackedRuntimeSessionStore
	seahorsePaths   []string
	engines         []*seahorse.Engine
}

func TestNewCodingRuntimeProfileWithStoreFactoryRejectsTypedNil(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	layout, err := NewCodingRuntimeLayout(
		"thread-typed-nil",
		executionRoot,
		filepath.Join(root, "state"),
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	var factory *trackingCodingRuntimeStoreFactory
	profile, err := NewCodingRuntimeProfileWithStoreFactory(
		factory,
		CodingRuntimeBinding{AgentID: "main", Layout: layout},
	)
	if err == nil || !strings.Contains(err.Error(), "store factory is required") {
		t.Fatalf("NewCodingRuntimeProfileWithStoreFactory() = %#v, %v, want typed-nil rejection", profile, err)
	}
}

func (f *trackingCodingRuntimeStoreFactory) NewSessionStore(layout CodingRuntimeLayout) (session.SessionStore, error) {
	f.sessionCalls++
	if f.sessionCalls == f.failSessionAt {
		return nil, errInjectedRuntimeStore
	}
	if f.nilSession {
		return nil, nil
	}
	if f.typedNilSession {
		var store *trackedRuntimeSessionStore
		return store, nil
	}
	store, err := f.delegate.NewSessionStore(layout)
	if err != nil {
		return nil, err
	}
	tracked := &trackedRuntimeSessionStore{SessionStore: store}
	f.sessions = append(f.sessions, tracked)
	return tracked, nil
}

func (f *trackingCodingRuntimeStoreFactory) NewSeahorseEngine(
	config seahorse.Config,
	complete seahorse.CompleteFn,
) (*seahorse.Engine, error) {
	f.seahorseCalls++
	f.seahorsePaths = append(f.seahorsePaths, config.DBPath)
	if f.failSeahorse || f.seahorseCalls == f.failSeahorseAt {
		return nil, errInjectedRuntimeStore
	}
	if f.nilSeahorse {
		return nil, nil
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

type promptCapturingProvider struct {
	mu       sync.Mutex
	messages []providers.Message
}

type codingPromptAttemptProvider struct {
	mu       sync.Mutex
	messages []providers.Message
	err      error
}

func (p *codingPromptAttemptProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.messages = cloneProviderMessages(messages)
	err := p.err
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &providers.LLMResponse{Content: "captured response", FinishReason: "stop"}, nil
}

func (p *codingPromptAttemptProvider) GetDefaultModel() string { return "captured-model" }

func (p *codingPromptAttemptProvider) Messages() []providers.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneProviderMessages(p.messages)
}

func (p *promptCapturingProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append([]providers.Message(nil), messages...)
	return &providers.LLMResponse{Content: "captured response"}, nil
}

func (p *promptCapturingProvider) GetDefaultModel() string { return "captured-model" }

func (p *promptCapturingProvider) Messages() []providers.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]providers.Message(nil), p.messages...)
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

func TestAgentInstanceCloseAggregatesOwnedRuntimeErrors(t *testing.T) {
	toolErr := errors.New("tool close failed")
	sessionErr := errors.New("session close failed")
	closer := &failingRuntimeCloser{err: toolErr}
	store := &failingRuntimeSessionStore{err: sessionErr}
	agent := &AgentInstance{Sessions: store, ownedToolClosers: []interface{ Close() error }{closer}}

	err := agent.Close()
	if !errors.Is(err, toolErr) || !errors.Is(err, sessionErr) {
		t.Fatalf("Close() error = %v, want joined tool and session errors", err)
	}
	if secondErr := agent.Close(); !errors.Is(secondErr, toolErr) || !errors.Is(secondErr, sessionErr) {
		t.Fatalf("second Close() error = %v, want stable joined error", secondErr)
	}
	if closer.closeCount != 1 || store.closeCount != 1 {
		t.Fatalf("close counts = tool:%d session:%d, want one each", closer.closeCount, store.closeCount)
	}
}

func TestNewCodingAgentLoopSeparatesExecutionAndState(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	stateRoot := filepath.Join(root, "private", "main")
	layout, err := NewCodingRuntimeLayout(
		"thread-1",
		executionRoot,
		stateRoot,
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	profile, err := NewCodingRuntimeProfile(CodingRuntimeBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfile() error = %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(root, "ignored-default")
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Tools.Exec.EnableDenyPatterns = true
	cfg.Tools.Exec.AllowRemote = true
	cfg.Tools.Exec.PermissionMode = "read_only"
	cfg.Tools.Exec.CustomAllowPatterns = []string{"[invalid-project-policy"}
	cfg.Tools.Exec.CustomDenyPatterns = []string{"project-policy-must-not-load"}
	cfg.Tools.MCP.Enabled = true
	cfg.Hooks.Enabled = true
	hookMarker := filepath.Join(root, "configured-hook-ran")
	cfg.Hooks.Processes = map[string]config.ProcessHookConfig{
		"project-extension": {
			Enabled: true,
			Command: []string{"sh", "-c", `touch "$1"`, "hook", hookMarker},
		},
	}
	loop, err := NewCodingAgentLoop(
		cfg,
		bus.NewMessageBus(),
		&mockProvider{},
		profile,
	)
	if err != nil {
		t.Fatalf("NewCodingAgentLoop() error = %v", err)
	}
	t.Cleanup(loop.Close)

	agent := loop.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is nil")
	}
	if agent.Workspace != layout.ExecutionRoot() {
		t.Fatalf("Workspace = %q, want execution root %q", agent.Workspace, layout.ExecutionRoot())
	}
	if agent.CodingLayout.StateRoot() != layout.StateRoot() {
		t.Fatalf("CodingLayout.StateRoot() = %q, want %q", agent.CodingLayout.StateRoot(), layout.StateRoot())
	}
	if got := agent.Tools.List(); !slices.Equal(got, codingRuntimeToolNames) {
		t.Fatalf("coding tools = %v, want %v", got, codingRuntimeToolNames)
	}
	originalExec, ok := agent.Tools.Get("exec")
	if !ok {
		t.Fatal("coding exec tool is missing")
	}
	if !newTestPipeline(loop).trustAllTools {
		t.Fatal("coding pipeline did not select trusted tool execution")
	}
	if !cfg.Tools.Exec.EnableDenyPatterns || !cfg.Tools.Exec.AllowRemote ||
		cfg.Tools.Exec.PermissionMode != "read_only" ||
		!slices.Equal(cfg.Tools.Exec.CustomAllowPatterns, []string{"[invalid-project-policy"}) ||
		!slices.Equal(cfg.Tools.Exec.CustomDenyPatterns, []string{"project-policy-must-not-load"}) {
		t.Fatalf("coding construction mutated persisted exec config: %#v", cfg.Tools.Exec)
	}
	loop.RegisterTool(&echoTextTool{})
	agent.Tools.Register(&allowlistTestTool{name: "exec"})
	agent.Tools.RegisterHidden(&allowlistTestTool{name: "hidden-extra"})
	agent.Tools.Unregister("read_file")
	agent.Tools.SetAllowlist([]string{})
	if gotExec, ok := agent.Tools.Get("exec"); !ok || gotExec != originalExec {
		t.Fatal("sealed coding registry replaced its trusted exec tool")
	}
	admittedRegistry := agent.Tools
	agent.Tools = admittedRegistry.Clone()
	if agent.isAdmittedTrustedToolRegistry(agent.Tools) {
		t.Fatal("cloned replacement registry retained coding trust")
	}
	agent.Tools = admittedRegistry
	if !agent.isAdmittedTrustedToolRegistry(agent.Tools) {
		t.Fatal("restored admitted registry lost coding trust")
	}
	if err := loop.RegisterRuntimeTool("extra", nil); err == nil {
		t.Fatal("coding runtime admitted a dynamic runtime tool")
	}
	if err := loop.MountHook(HookRegistration{}); err == nil {
		t.Fatal("coding runtime admitted an in-process hook")
	}
	if err := loop.MountProcessHook(context.Background(), "extra", ProcessHookOptions{}); err == nil {
		t.Fatal("coding runtime admitted a process hook")
	}
	if err := loop.ensureHooksInitialized(context.Background()); err != nil {
		t.Fatalf("coding hook initialization should be disabled: %v", err)
	}
	if _, err := os.Stat(hookMarker); !os.IsNotExist(err) {
		t.Fatalf("coding runtime executed configured process hook: %v", err)
	}
	if got := agent.Tools.List(); !slices.Equal(got, codingRuntimeToolNames) {
		t.Fatalf("coding catalog changed after dynamic injection: %v", got)
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

func TestCodingRuntimeUsesIsolatedPromptAndSessionIdentity(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	stateRoot := filepath.Join(root, "state")
	if err := os.MkdirAll(executionRoot, 0o755); err != nil {
		t.Fatalf("create execution root: %v", err)
	}
	personalFiles := map[string]string{
		"AGENT.md": "---\nname: Project Persona\nmodel: project-model\nskills: [personal]\n---\nPERSONAL AGENT BODY",
		"SOUL.md":  "PERSONAL SOUL",
		"USER.md":  "PERSONAL USER",
	}
	for name, content := range personalFiles {
		if err := os.WriteFile(filepath.Join(executionRoot, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	layout, err := NewCodingRuntimeLayout(
		"thread-isolated",
		executionRoot,
		stateRoot,
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	profile, err := NewCodingRuntimeProfile(CodingRuntimeBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfile() error = %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.Defaults.Provider = "test-provider"
	cfg.Agents.Defaults.ModelName = "configured-model"
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "configured-model",
		Provider:  "test-provider",
		Model:     "configured-model",
		Enabled:   true,
	}}
	cfg.Agents.Defaults.TurnProfile = config.TurnProfileConfig{
		Enabled: true,
		Skills:  config.TurnProfileBlock{Mode: config.TurnProfileModeOff},
		Tools: config.TurnProfileBlock{
			Mode:  config.TurnProfileModeCustom,
			Allow: []string{"read_file"},
		},
	}
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Name: "Configured Persona", Default: true}}
	provider := &promptCapturingProvider{}
	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), provider, profile)
	if err != nil {
		t.Fatalf("NewCodingAgentLoop() error = %v", err)
	}
	t.Cleanup(loop.Close)

	agent := loop.GetRegistry().GetDefaultAgent()
	if agent.Name != "MintClaw coding agent" || agent.Model != "configured-model" || agent.Definition.Agent != nil {
		t.Fatalf(
			"coding identity = name:%q model:%q definition:%#v",
			agent.Name,
			agent.Model,
			agent.Definition.Agent,
		)
	}
	messages := agent.ContextBuilder.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage:    "fix the bug",
		Channel:           "telegram",
		ChatID:            "personal-chat",
		SenderID:          "personal-sender",
		SenderDisplayName: "Personal Sender",
	})
	if len(messages) != 2 || messages[0].Role != "system" || messages[1].Role != "user" {
		t.Fatalf("coding prompt messages = %#v", messages)
	}
	wantSystem := strings.Join([]string{
		codingAgentBaseInstructions,
		"# Project\n\nProject root: " + layout.ExecutionRoot(),
		"# Coding thread\n\nThread ID: thread-isolated\n" +
			"Session key: coding:thread-isolated\n" +
			"Working directory: " + layout.ExecutionRoot() + "\n" +
			"Trust mode: yolo\nModel: configured-model",
	}, "\n\n---\n\n")
	wantSystem += "\n\n---\n\n" + codingworkspace.RenderPrompt(
		codingworkspace.Capture(t.Context(), layout.ExecutionRoot(), layout.ExecutionRoot(), codingworkspace.Limits{}),
		0,
	)
	if messages[0].Content != wantSystem {
		t.Fatalf("coding system prompt =\n%s\nwant:\n%s", messages[0].Content, wantSystem)
	}
	for _, forbidden := range []string{
		"PERSONAL AGENT BODY",
		"PERSONAL SOUL",
		"PERSONAL USER",
		"PERSONAL IDENTITY",
		"personal-chat",
		"personal-sender",
		"Personal Sender",
		"helpful AI assistant",
		"# Memory",
	} {
		if strings.Contains(messages[0].Content, forbidden) {
			t.Fatalf("coding system prompt contains personal context %q:\n%s", forbidden, messages[0].Content)
		}
	}
	if len(messages[0].SystemParts) != 2 || messages[0].SystemParts[0].CacheControl == nil ||
		messages[0].SystemParts[0].CacheControl.Type != "ephemeral" ||
		messages[0].SystemParts[1].CacheControl != nil {
		t.Fatalf("coding cache blocks = %#v, want stable prefix then dynamic context", messages[0].SystemParts)
	}
	secondPrompt := agent.ContextBuilder.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage: "continue",
		CodingContext: CodingPromptContext{
			WorkingDirectory: filepath.Join(layout.ExecutionRoot(), "nested"),
			Provider:         "test-provider",
		},
	})
	if secondPrompt[0].SystemParts[0].Text != messages[0].SystemParts[0].Text {
		t.Fatal("coding stable prefix changed with turn metadata")
	}
	if secondPrompt[0].SystemParts[1].Text == messages[0].SystemParts[1].Text ||
		!strings.Contains(secondPrompt[0].SystemParts[1].Text, "Provider: test-provider") {
		t.Fatalf("coding dynamic block did not reflect turn metadata: %#v", secondPrompt[0].SystemParts[1])
	}

	const sessionKey = "coding:thread-isolated"
	if _, err := loop.ProcessDirect(t.Context(), "wrong thread", "coding:another-thread"); err == nil ||
		!strings.Contains(err.Error(), "has no admitted thread") {
		t.Fatalf("ProcessDirect(wrong thread) error = %v, want thread rejection", err)
	}
	if _, err := loop.ProcessDirect(t.Context(), "persist only here", sessionKey); err != nil {
		t.Fatalf("ProcessDirect() error = %v", err)
	}
	providerMessages := provider.Messages()
	if len(providerMessages) < 2 || providerMessages[0].Role != "system" {
		t.Fatalf("provider messages = %#v, want system and user", providerMessages)
	}
	for _, required := range []string{
		codingAgentBaseInstructions,
		"Project root: " + layout.ExecutionRoot(),
		"Working directory: " + layout.ExecutionRoot(),
		"Thread ID: thread-isolated",
		"Session key: " + sessionKey,
		"Trust mode: yolo",
		"Model: configured-model",
		"Provider: test-provider",
	} {
		if !strings.Contains(providerMessages[0].Content, required) {
			t.Fatalf("provider system prompt missing %q:\n%s", required, providerMessages[0].Content)
		}
	}
	if strings.Contains(providerMessages[0].Content, "PERSONAL AGENT BODY") ||
		strings.Contains(providerMessages[0].Content, "helpful AI assistant") {
		t.Fatalf("provider observed personal context:\n%s", providerMessages[0].Content)
	}
	if history := agent.Sessions.GetHistory(sessionKey); len(history) != 2 {
		t.Fatalf("coding history length = %d, want user and assistant", len(history))
	}
	if history := agent.Sessions.GetHistory(session.BuildMainSessionKey(agent.ID)); len(history) != 0 {
		t.Fatalf("personal main history = %#v, want empty", history)
	}
	if sessions := agent.Sessions.ListSessions(); !slices.Equal(sessions, []string{sessionKey}) {
		t.Fatalf("coding sessions = %v, want only %q", sessions, sessionKey)
	}
}

func TestCodingDirectResolvesEachAdmittedThread(t *testing.T) {
	root := t.TempDir()
	mainLayout, err := NewCodingRuntimeLayout(
		"thread-main",
		filepath.Join(root, "project-main"),
		filepath.Join(root, "state-main"),
		[]string{filepath.Join(root, "project-main")},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout(main) error = %v", err)
	}
	supportLayout, err := NewCodingRuntimeLayout(
		"thread-support",
		filepath.Join(root, "project-support"),
		filepath.Join(root, "state-support"),
		[]string{filepath.Join(root, "project-support")},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout(support) error = %v", err)
	}
	profile, err := NewCodingRuntimeProfile(
		CodingRuntimeBinding{AgentID: "main", Layout: mainLayout},
		CodingRuntimeBinding{AgentID: "support", Layout: supportLayout},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	}
	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err != nil {
		t.Fatalf("NewCodingAgentLoop() error = %v", err)
	}
	t.Cleanup(loop.Close)

	for _, target := range []struct {
		agentID    string
		sessionKey string
	}{
		{agentID: "main", sessionKey: "coding:thread-main"},
		{agentID: "support", sessionKey: "coding:thread-support"},
	} {
		if _, err := loop.ProcessDirect(t.Context(), "turn for "+target.agentID, target.sessionKey); err != nil {
			t.Fatalf("ProcessDirect(%s) error = %v", target.agentID, err)
		}
		agent, ok := loop.GetRegistry().GetAgent(target.agentID)
		if !ok {
			t.Fatalf("agent %q is missing", target.agentID)
		}
		if history := agent.Sessions.GetHistory(target.sessionKey); len(history) != 2 {
			t.Fatalf("%s history length = %d, want 2", target.agentID, len(history))
		}
		if sessions := agent.Sessions.ListSessions(); !slices.Equal(sessions, []string{target.sessionKey}) {
			t.Fatalf("%s sessions = %v, want only %q", target.agentID, sessions, target.sessionKey)
		}
	}
	if _, err := loop.ProcessDirect(t.Context(), "unknown", "coding:thread-unknown"); err == nil ||
		!strings.Contains(err.Error(), "has no admitted thread") {
		t.Fatalf("ProcessDirect(unknown) error = %v, want fail-closed thread rejection", err)
	}
}

func TestNewCodingRuntimeProfileRejectsStateInsideAnotherExecutionRoot(t *testing.T) {
	root := t.TempDir()
	firstExecution := filepath.Join(root, "project-a")
	secondExecution := filepath.Join(root, "project-b")
	firstLayout, err := NewCodingRuntimeLayout(
		"thread-a",
		firstExecution,
		filepath.Join(secondExecution, ".mintclaw", "thread-a"),
		[]string{firstExecution},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout(first) error = %v", err)
	}
	secondState := filepath.Join(root, "state", "thread-b")
	secondLayout, err := NewCodingRuntimeLayout(
		"thread-b",
		secondExecution,
		secondState,
		[]string{secondExecution},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout(second) error = %v", err)
	}

	profile, err := NewCodingRuntimeProfile(
		CodingRuntimeBinding{AgentID: "main", Layout: firstLayout},
		CodingRuntimeBinding{AgentID: "support", Layout: secondLayout},
	)
	if err == nil {
		t.Fatalf("NewCodingRuntimeProfile() = %#v, want cross-agent root rejection", profile)
	}
	if _, statErr := os.Stat(firstLayout.StateRoot()); !os.IsNotExist(statErr) {
		t.Fatalf("rejected profile created first state root: %v", statErr)
	}
	if _, statErr := os.Stat(secondState); !os.IsNotExist(statErr) {
		t.Fatalf("rejected profile created second state root: %v", statErr)
	}
}

func TestNewCodingRuntimeProfileRejectsOverlappingStateRootsForDistinctThreads(t *testing.T) {
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
			firstLayout, err := NewCodingRuntimeLayout(
				"thread-a",
				filepath.Join(root, "project-a"),
				firstState,
				[]string{filepath.Join(root, "project-a")},
			)
			if err != nil {
				t.Fatalf("NewCodingRuntimeLayout(first) error = %v", err)
			}
			secondLayout, err := NewCodingRuntimeLayout(
				"thread-b",
				filepath.Join(root, "project-b"),
				test.secondState(firstState),
				[]string{filepath.Join(root, "project-b")},
			)
			if err != nil {
				t.Fatalf("NewCodingRuntimeLayout(second) error = %v", err)
			}

			profile, err := NewCodingRuntimeProfile(
				CodingRuntimeBinding{AgentID: "main", Layout: firstLayout},
				CodingRuntimeBinding{AgentID: "support", Layout: secondLayout},
			)
			if err == nil {
				t.Fatalf("NewCodingRuntimeProfile() = %#v, want overlapping-state rejection", profile)
			}
			if _, statErr := os.Stat(firstState); !os.IsNotExist(statErr) {
				t.Fatalf("rejected profile created state root: %v", statErr)
			}
		})
	}
}

func TestNewCodingRuntimeProfileRejectsSharedExecutionRootForDistinctThreads(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	first, err := NewCodingRuntimeLayout(
		"thread-one",
		executionRoot,
		filepath.Join(root, "state-one"),
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout(first) error = %v", err)
	}
	second, err := NewCodingRuntimeLayout(
		"thread-two",
		executionRoot,
		filepath.Join(root, "state-two"),
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout(second) error = %v", err)
	}
	if _, err := NewCodingRuntimeProfile(
		CodingRuntimeBinding{AgentID: "main", Layout: first},
		CodingRuntimeBinding{AgentID: "support", Layout: second},
	); err == nil || !strings.Contains(err.Error(), "share an execution root") {
		t.Fatalf("NewCodingRuntimeProfile(shared execution root) error = %v", err)
	}
}

func TestNewCodingAgentLoopRejectsUnusableStatePaths(t *testing.T) {
	for _, test := range []struct {
		name      string
		blockPath func(CodingRuntimeLayout) string
	}{
		{
			name: "sessions root",
			blockPath: func(layout CodingRuntimeLayout) string {
				return layout.StatePaths().SessionsRoot
			},
		},
		{
			name: "context root",
			blockPath: func(layout CodingRuntimeLayout) string {
				return layout.StatePaths().ContextRoot
			},
		},
		{
			name: "memory root",
			blockPath: func(layout CodingRuntimeLayout) string {
				return layout.StatePaths().MemoryRoot
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			executionRoot := filepath.Join(root, "project")
			layout, err := NewCodingRuntimeLayout(
				"thread-state-error",
				executionRoot,
				filepath.Join(root, "state"),
				[]string{executionRoot},
			)
			if err != nil {
				t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
			}
			blockedPath := test.blockPath(layout)
			if err := os.MkdirAll(filepath.Dir(blockedPath), 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if err := os.WriteFile(blockedPath, []byte("not a directory"), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			profile, err := NewCodingRuntimeProfile(CodingRuntimeBinding{AgentID: "main", Layout: layout})
			if err != nil {
				t.Fatalf("NewCodingRuntimeProfile() error = %v", err)
			}
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.ContextManager = "none"

			loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
			if err == nil {
				if loop != nil {
					loop.Close()
				}
				t.Fatal("NewCodingAgentLoop() error = nil, want unusable-state error")
			}
			if loop != nil {
				t.Fatalf("NewCodingAgentLoop() loop = %T, want nil", loop)
			}
			if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
				t.Fatalf("failed construction created execution root: %v", statErr)
			}
		})
	}
}

func TestNewCodingAgentLoopPreflightsAllBindings(t *testing.T) {
	root := t.TempDir()
	mainExecution := filepath.Join(root, "main-project")
	mainState := filepath.Join(root, "state", "main")
	mainLayout, err := NewCodingRuntimeLayout(
		"thread-main",
		mainExecution,
		mainState,
		[]string{mainExecution},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	profile, err := NewCodingRuntimeProfile(CodingRuntimeBinding{AgentID: "main", Layout: mainLayout})
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	}

	loop, err := NewCodingAgentLoop(
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
		t.Fatal("NewCodingAgentLoop() error = nil, want missing-binding error")
	}
	if loop != nil {
		t.Fatalf("NewCodingAgentLoop() loop = %T, want nil", loop)
	}
	if _, statErr := os.Stat(mainExecution); !os.IsNotExist(statErr) {
		t.Fatalf("failed preflight created execution root: stat error = %v", statErr)
	}
	if _, statErr := os.Stat(mainState); !os.IsNotExist(statErr) {
		t.Fatalf("failed preflight created state root: stat error = %v", statErr)
	}
}

func TestNewCodingAgentLoopPreflightsLaterStateBeforeConstruction(t *testing.T) {
	root := t.TempDir()
	mainExecution := filepath.Join(root, "main-project")
	mainState := filepath.Join(root, "state", "main")
	mainLayout, err := NewCodingRuntimeLayout(
		"thread-main",
		mainExecution,
		mainState,
		[]string{mainExecution},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout(main) error = %v", err)
	}
	supportExecution := filepath.Join(root, "support-project")
	supportState := filepath.Join(root, "state", "support")
	supportLayout, err := NewCodingRuntimeLayout(
		"thread-support",
		supportExecution,
		supportState,
		[]string{supportExecution},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout(support) error = %v", err)
	}
	if err := os.MkdirAll(supportState, 0o755); err != nil {
		t.Fatalf("MkdirAll(support state) error = %v", err)
	}
	if err := os.WriteFile(supportLayout.StatePaths().SessionsRoot, []byte("blocked"), 0o600); err != nil {
		t.Fatalf("WriteFile(blocked sessions) error = %v", err)
	}
	profile, err := NewCodingRuntimeProfile(
		CodingRuntimeBinding{AgentID: "main", Layout: mainLayout},
		CodingRuntimeBinding{AgentID: "support", Layout: supportLayout},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	}

	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err == nil {
		if loop != nil {
			loop.Close()
		}
		t.Fatal("NewCodingAgentLoop() error = nil, want later-state preflight error")
	}
	if loop != nil {
		t.Fatalf("NewCodingAgentLoop() loop = %T, want nil", loop)
	}
	if _, statErr := os.Stat(mainState); !os.IsNotExist(statErr) {
		t.Fatalf("failed preflight created earlier thread state: %v", statErr)
	}
	if _, statErr := os.Stat(mainExecution); !os.IsNotExist(statErr) {
		t.Fatalf("failed preflight created earlier execution root: %v", statErr)
	}
}

func TestNewCodingAgentLoopRevalidatesPhysicalStateIsolation(t *testing.T) {
	root := t.TempDir()
	mainExecution := filepath.Join(root, "main-project")
	mainState := filepath.Join(root, "state-main")
	mainLayout, err := NewCodingRuntimeLayout(
		"thread-main",
		mainExecution,
		mainState,
		[]string{mainExecution},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout(main) error = %v", err)
	}
	supportExecution := filepath.Join(root, "support-project")
	supportState := filepath.Join(root, "state-support")
	supportLayout, err := NewCodingRuntimeLayout(
		"thread-support",
		supportExecution,
		supportState,
		[]string{supportExecution},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout(support) error = %v", err)
	}
	profile, err := NewCodingRuntimeProfile(
		CodingRuntimeBinding{AgentID: "main", Layout: mainLayout},
		CodingRuntimeBinding{AgentID: "support", Layout: supportLayout},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfile() error = %v", err)
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

	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err == nil {
		if loop != nil {
			loop.Close()
		}
		t.Fatal("NewCodingAgentLoop() error = nil, want physical-root isolation error")
	}
	if loop != nil {
		t.Fatalf("NewCodingAgentLoop() loop = %T, want nil", loop)
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

func TestNewCodingAgentLoopRejectsDerivedStateSymlinks(t *testing.T) {
	for _, test := range []struct {
		name       string
		linkedPath func(CodingRuntimeLayout) string
	}{
		{
			name: "sessions",
			linkedPath: func(layout CodingRuntimeLayout) string {
				return layout.StatePaths().SessionsRoot
			},
		},
		{
			name: "context",
			linkedPath: func(layout CodingRuntimeLayout) string {
				return layout.StatePaths().ContextRoot
			},
		},
		{
			name: "memory",
			linkedPath: func(layout CodingRuntimeLayout) string {
				return layout.StatePaths().MemoryRoot
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			executionRoot := filepath.Join(root, "project")
			stateRoot := filepath.Join(root, "state")
			layout, err := NewCodingRuntimeLayout(
				"thread-symlink",
				executionRoot,
				stateRoot,
				[]string{executionRoot},
			)
			if err != nil {
				t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
			}
			profile, err := NewCodingRuntimeProfile(CodingRuntimeBinding{AgentID: "main", Layout: layout})
			if err != nil {
				t.Fatalf("NewCodingRuntimeProfile() error = %v", err)
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

			loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
			if err == nil {
				if loop != nil {
					loop.Close()
				}
				t.Fatal("NewCodingAgentLoop() error = nil, want derived-symlink rejection")
			}
			if loop != nil {
				t.Fatalf("NewCodingAgentLoop() loop = %T, want nil", loop)
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

func TestNewCodingAgentLoopRejectsSeahorseDatabaseSymlink(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	layout, err := NewCodingRuntimeLayout(
		"thread-db-symlink",
		executionRoot,
		filepath.Join(root, "state"),
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	profile, err := NewCodingRuntimeProfile(CodingRuntimeBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfile() error = %v", err)
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

	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err == nil {
		if loop != nil {
			loop.Close()
		}
		t.Fatal("NewCodingAgentLoop() error = nil, want Seahorse symlink rejection")
	}
	if loop != nil {
		t.Fatalf("NewCodingAgentLoop() loop = %T, want nil", loop)
	}
	info, statErr := os.Stat(target)
	if statErr != nil || info.Size() != 0 {
		t.Fatalf("rejected construction mutated symlink target: info=%v error=%v", info, statErr)
	}
}

func TestNewCodingAgentLoopRejectsInvalidOperationalLeaves(t *testing.T) {
	for _, test := range []struct {
		name    string
		path    func(CodingRuntimeStatePaths) string
		content []byte
		link    bool
	}{
		{name: "runtime state symlink", path: func(paths CodingRuntimeStatePaths) string {
			return paths.RuntimeStateFile
		}, link: true},
		{name: "corrupt runtime state", path: func(paths CodingRuntimeStatePaths) string {
			return paths.RuntimeStateFile
		}, content: []byte("{")},
		{name: "corrupt task registry", path: func(paths CodingRuntimeStatePaths) string {
			return paths.TaskRegistryFile
		}, content: []byte("{")},
		{name: "corrupt interaction registry", path: func(paths CodingRuntimeStatePaths) string {
			return paths.InteractionFile
		}, content: []byte("{")},
		{name: "invalid interaction key", path: func(paths CodingRuntimeStatePaths) string {
			return paths.InteractionKeyFile
		}, content: []byte("short")},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			executionRoot := filepath.Join(root, "project")
			layout, err := NewCodingRuntimeLayout(
				"thread-operational",
				executionRoot,
				filepath.Join(root, "state"),
				[]string{executionRoot},
			)
			if err != nil {
				t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
			}
			leaf := test.path(layout.StatePaths())
			if err := os.MkdirAll(filepath.Dir(leaf), 0o700); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if test.link {
				target := filepath.Join(root, "outside-state")
				if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
					t.Fatalf("WriteFile(target) error = %v", err)
				}
				if err := os.Symlink(target, leaf); err != nil {
					t.Skipf("Symlink() unavailable: %v", err)
				}
			} else if err := os.WriteFile(leaf, test.content, 0o600); err != nil {
				t.Fatalf("WriteFile(leaf) error = %v", err)
			}
			profile, err := NewCodingRuntimeProfile(CodingRuntimeBinding{AgentID: "main", Layout: layout})
			if err != nil {
				t.Fatalf("NewCodingRuntimeProfile() error = %v", err)
			}
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.ContextManager = "none"
			loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
			if err == nil {
				if loop != nil {
					loop.Close()
				}
				t.Fatal("NewCodingAgentLoop() error = nil, want invalid-leaf rejection")
			}
			if loop != nil {
				t.Fatalf("NewCodingAgentLoop() loop = %T, want nil", loop)
			}
			if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
				t.Fatalf("failed operational preflight created execution root: %v", statErr)
			}
		})
	}
}

func TestNewCodingAgentLoopChecksLaterStateCreatability(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not enforce Unix directory mode bits")
	}
	root := t.TempDir()
	mainExecution := filepath.Join(root, "main-project")
	mainState := filepath.Join(root, "state-main")
	mainLayout, err := NewCodingRuntimeLayout(
		"thread-main",
		mainExecution,
		mainState,
		[]string{mainExecution},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout(main) error = %v", err)
	}
	supportExecution := filepath.Join(root, "support-project")
	supportState := filepath.Join(root, "state-support")
	supportLayout, err := NewCodingRuntimeLayout(
		"thread-support",
		supportExecution,
		supportState,
		[]string{supportExecution},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout(support) error = %v", err)
	}
	if err := os.MkdirAll(supportState, 0o500); err != nil {
		t.Fatalf("MkdirAll(support state) error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(supportState, 0o700)
	})
	profile, err := NewCodingRuntimeProfile(
		CodingRuntimeBinding{AgentID: "main", Layout: mainLayout},
		CodingRuntimeBinding{AgentID: "support", Layout: supportLayout},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	}

	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err == nil {
		if loop != nil {
			loop.Close()
		}
		t.Fatal("NewCodingAgentLoop() error = nil, want state-creatability error")
	}
	if loop != nil {
		t.Fatalf("NewCodingAgentLoop() loop = %T, want nil", loop)
	}
	if _, statErr := os.Stat(mainState); !os.IsNotExist(statErr) {
		t.Fatalf("failed creatability preflight created earlier thread state: %v", statErr)
	}
}

func TestNewCodingAgentLoopDoesNotConvertJSONSessions(t *testing.T) {
	root := t.TempDir()
	mainExecution := filepath.Join(root, "main-project")
	mainLayout, err := NewCodingRuntimeLayout(
		"thread-main",
		mainExecution,
		filepath.Join(root, "state-main"),
		[]string{mainExecution},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout(main) error = %v", err)
	}
	supportExecution := filepath.Join(root, "support-project")
	supportLayout, err := NewCodingRuntimeLayout(
		"thread-support",
		supportExecution,
		filepath.Join(root, "state-support"),
		[]string{supportExecution},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout(support) error = %v", err)
	}
	supportSessions := supportLayout.StatePaths().SessionsRoot
	if err := os.MkdirAll(filepath.Join(supportSessions, "blocked.meta.json"), 0o755); err != nil {
		t.Fatalf("MkdirAll(blocked metadata) error = %v", err)
	}
	jsonSession := filepath.Join(supportSessions, "blocked.json")
	if err := os.WriteFile(jsonSession, []byte(`{"key":"blocked","messages":[]}`), 0o600); err != nil {
		t.Fatalf("WriteFile(JSON session) error = %v", err)
	}
	profile, err := NewCodingRuntimeProfile(
		CodingRuntimeBinding{AgentID: "main", Layout: mainLayout},
		CodingRuntimeBinding{AgentID: "support", Layout: supportLayout},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	}

	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err != nil {
		t.Fatalf("NewCodingAgentLoop() error = %v", err)
	}
	t.Cleanup(loop.Close)
	if _, statErr := os.Stat(jsonSession); statErr != nil {
		t.Fatalf("coding runtime mutated JSON session: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(supportSessions, "blocked.jsonl")); !os.IsNotExist(statErr) {
		t.Fatalf("coding runtime created converted JSONL: %v", statErr)
	}
}

func TestNewCodingAgentLoopRoutesCodingSeahorseToStateRoot(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	stateRoot := filepath.Join(root, "state")
	layout, err := NewCodingRuntimeLayout(
		"thread-1",
		executionRoot,
		stateRoot,
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	profile, err := NewCodingRuntimeProfile(CodingRuntimeBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	if contextManagerConfigName(cfg) != "seahorse" {
		t.Fatalf("default context manager = %q, want seahorse", contextManagerConfigName(cfg))
	}

	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err != nil {
		t.Fatalf("NewCodingAgentLoop() error = %v", err)
	}
	t.Cleanup(loop.Close)
	wantTools := append([]string(nil), codingRuntimeToolNames...)
	wantTools = append(wantTools, "short_expand", "short_grep")
	slices.Sort(wantTools)
	if got := loop.GetRegistry().GetDefaultAgent().Tools.List(); !slices.Equal(got, wantTools) {
		t.Fatalf("Seahorse coding tools = %v, want %v", got, wantTools)
	}
	if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
		t.Fatalf("coding construction created execution root: %v", statErr)
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

func TestCodingRuntimeProfileSeparatesSeahorseDatabasesByCodingThread(t *testing.T) {
	root := t.TempDir()
	bindings := make([]CodingRuntimeBinding, 0, 2)
	wantPaths := make([]string, 0, 2)
	for _, spec := range []struct {
		agentID string
		thread  string
	}{
		{agentID: "main", thread: "thread-one"},
		{agentID: "support", thread: "thread-two"},
	} {
		executionRoot := filepath.Join(root, "projects", spec.thread)
		layout, err := NewCodingRuntimeLayout(
			spec.thread,
			executionRoot,
			filepath.Join(root, "state", spec.thread),
			[]string{executionRoot},
		)
		if err != nil {
			t.Fatalf("NewCodingRuntimeLayout(%s) error = %v", spec.thread, err)
		}
		bindings = append(bindings, CodingRuntimeBinding{AgentID: spec.agentID, Layout: layout})
		wantPaths = append(wantPaths, filepath.Join(layout.StatePaths().ContextRoot, "seahorse.db"))
	}
	factory := &trackingCodingRuntimeStoreFactory{}
	profile, err := NewCodingRuntimeProfileWithStoreFactory(factory, bindings...)
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfileWithStoreFactory() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	}

	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err != nil {
		t.Fatalf("NewCodingAgentLoop() error = %v", err)
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

func TestCodingRuntimeProfileStoreConstructionRollsBackEarlierSessions(t *testing.T) {
	root := t.TempDir()
	bindings := make([]CodingRuntimeBinding, 0, 2)
	for _, id := range []string{"main", "support"} {
		executionRoot := filepath.Join(root, "projects", id)
		layout, err := NewCodingRuntimeLayout(
			"thread-"+id,
			executionRoot,
			filepath.Join(root, "state", id),
			[]string{executionRoot},
		)
		if err != nil {
			t.Fatalf("NewCodingRuntimeLayout(%s) error = %v", id, err)
		}
		bindings = append(bindings, CodingRuntimeBinding{AgentID: id, Layout: layout})
	}
	factory := &trackingCodingRuntimeStoreFactory{failSessionAt: 2}
	profile, err := NewCodingRuntimeProfileWithStoreFactory(factory, bindings...)
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfileWithStoreFactory() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	}

	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if !errors.Is(err, errInjectedRuntimeStore) {
		if loop != nil {
			loop.Close()
		}
		t.Fatalf("NewCodingAgentLoop() error = %v, want injected failure", err)
	}
	if loop != nil {
		t.Fatalf("NewCodingAgentLoop() loop = %T, want nil", loop)
	}
	if len(factory.sessions) != 1 || factory.sessions[0].closeCount != 1 {
		t.Fatalf("opened sessions = %#v, want one store closed exactly once", factory.sessions)
	}
}

func TestCodingRuntimeProfileRejectsNilSessionStoreProducts(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*trackingCodingRuntimeStoreFactory)
	}{
		{
			name: "nil interface",
			configure: func(factory *trackingCodingRuntimeStoreFactory) {
				factory.nilSession = true
			},
		},
		{
			name: "typed nil",
			configure: func(factory *trackingCodingRuntimeStoreFactory) {
				factory.typedNilSession = true
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			executionRoot := filepath.Join(root, "project")
			layout, err := NewCodingRuntimeLayout(
				"thread-nil-session",
				executionRoot,
				filepath.Join(root, "state"),
				[]string{executionRoot},
			)
			if err != nil {
				t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
			}
			factory := &trackingCodingRuntimeStoreFactory{}
			test.configure(factory)
			profile, err := NewCodingRuntimeProfileWithStoreFactory(
				factory,
				CodingRuntimeBinding{AgentID: "main", Layout: layout},
			)
			if err != nil {
				t.Fatalf("NewCodingRuntimeProfileWithStoreFactory() error = %v", err)
			}
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.ContextManager = "none"

			loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
			if err == nil || !strings.Contains(err.Error(), "nil session store") {
				if loop != nil {
					loop.Close()
				}
				t.Fatalf("NewCodingAgentLoop() error = %v, want nil-store rejection", err)
			}
			if loop != nil {
				t.Fatalf("NewCodingAgentLoop() loop = %T, want nil", loop)
			}
		})
	}
}

func TestCodingRuntimeProfileContextConstructionFailureClosesCanonicalStore(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	layout, err := NewCodingRuntimeLayout(
		"thread-context-failure",
		executionRoot,
		filepath.Join(root, "state"),
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	factory := &trackingCodingRuntimeStoreFactory{failSeahorse: true}
	profile, err := NewCodingRuntimeProfileWithStoreFactory(
		factory,
		CodingRuntimeBinding{AgentID: "main", Layout: layout},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfileWithStoreFactory() error = %v", err)
	}
	cfg := config.DefaultConfig()

	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if !errors.Is(err, errInjectedRuntimeStore) {
		if loop != nil {
			loop.Close()
		}
		t.Fatalf("NewCodingAgentLoop() error = %v, want injected failure", err)
	}
	if loop != nil {
		t.Fatalf("NewCodingAgentLoop() loop = %T, want nil", loop)
	}
	if len(factory.sessions) != 1 || factory.sessions[0].closeCount != 1 {
		t.Fatalf("opened sessions = %#v, want canonical store closed exactly once", factory.sessions)
	}
	wantDB := filepath.Join(layout.StatePaths().ContextRoot, "seahorse.db")
	if len(factory.seahorsePaths) != 1 || factory.seahorsePaths[0] != wantDB {
		t.Fatalf("Seahorse paths = %v, want [%q]", factory.seahorsePaths, wantDB)
	}
}

func TestCodingRuntimeProfileNilSeahorseEngineClosesCanonicalStore(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	layout, err := NewCodingRuntimeLayout(
		"thread-nil-seahorse",
		executionRoot,
		filepath.Join(root, "state"),
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	factory := &trackingCodingRuntimeStoreFactory{nilSeahorse: true}
	profile, err := NewCodingRuntimeProfileWithStoreFactory(
		factory,
		CodingRuntimeBinding{AgentID: "main", Layout: layout},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfileWithStoreFactory() error = %v", err)
	}
	cfg := config.DefaultConfig()

	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err == nil || !strings.Contains(err.Error(), "nil Seahorse engine") {
		if loop != nil {
			loop.Close()
		}
		t.Fatalf("NewCodingAgentLoop() error = %v, want nil-engine rejection", err)
	}
	if loop != nil {
		t.Fatalf("NewCodingAgentLoop() loop = %T, want nil", loop)
	}
	if len(factory.sessions) != 1 || factory.sessions[0].closeCount != 1 {
		t.Fatalf("opened sessions = %#v, want canonical store closed exactly once", factory.sessions)
	}
}

func TestCodingRuntimeProfileLaterContextFailureClosesPartialManager(t *testing.T) {
	root := t.TempDir()
	bindings := make([]CodingRuntimeBinding, 0, 2)
	for _, id := range []string{"main", "support"} {
		executionRoot := filepath.Join(root, "projects", id)
		layout, err := NewCodingRuntimeLayout(
			"thread-context-"+id,
			executionRoot,
			filepath.Join(root, "state", id),
			[]string{executionRoot},
		)
		if err != nil {
			t.Fatalf("NewCodingRuntimeLayout(%s) error = %v", id, err)
		}
		bindings = append(bindings, CodingRuntimeBinding{AgentID: id, Layout: layout})
	}
	factory := &trackingCodingRuntimeStoreFactory{failSeahorseAt: 2}
	profile, err := NewCodingRuntimeProfileWithStoreFactory(factory, bindings...)
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfileWithStoreFactory() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	}

	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if !errors.Is(err, errInjectedRuntimeStore) {
		if loop != nil {
			loop.Close()
		}
		t.Fatalf("NewCodingAgentLoop() error = %v, want injected failure", err)
	}
	if loop != nil {
		t.Fatalf("NewCodingAgentLoop() loop = %T, want nil", loop)
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

func TestCodingRuntimeProfileRejectsCustomSeahorsePathAndRollsBack(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	layout, err := NewCodingRuntimeLayout(
		"thread-custom-db",
		executionRoot,
		filepath.Join(root, "state"),
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	factory := &trackingCodingRuntimeStoreFactory{}
	profile, err := NewCodingRuntimeProfileWithStoreFactory(
		factory,
		CodingRuntimeBinding{AgentID: "main", Layout: layout},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfileWithStoreFactory() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManagerConfig = []byte(`{"dbPath":"/tmp/not-owner-scoped.db"}`)

	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err == nil || !strings.Contains(err.Error(), "custom dbPath") {
		if loop != nil {
			loop.Close()
		}
		t.Fatalf("NewCodingAgentLoop() error = %v, want custom-dbPath rejection", err)
	}
	if len(factory.sessions) != 1 || factory.sessions[0].closeCount != 1 {
		t.Fatalf("opened sessions = %#v, want canonical store closed exactly once", factory.sessions)
	}
	if len(factory.seahorsePaths) != 0 {
		t.Fatalf("Seahorse factory called with rejected path: %v", factory.seahorsePaths)
	}
}

func TestCodingRuntimeProfileRejectsUnsupportedContextManagerBeforeStores(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	stateRoot := filepath.Join(root, "state")
	layout, err := NewCodingRuntimeLayout(
		"thread-unsupported-context",
		executionRoot,
		stateRoot,
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	factory := &trackingCodingRuntimeStoreFactory{}
	profile, err := NewCodingRuntimeProfileWithStoreFactory(
		factory,
		CodingRuntimeBinding{AgentID: "main", Layout: layout},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfileWithStoreFactory() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "custom"

	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err == nil || !strings.Contains(err.Error(), "no thread-scoped storage contract") {
		if loop != nil {
			loop.Close()
		}
		t.Fatalf("NewCodingAgentLoop() error = %v, want unsupported-context rejection", err)
	}
	if factory.sessionCalls != 0 || len(factory.seahorsePaths) != 0 {
		t.Fatalf(
			"rejected context manager constructed stores: sessions=%d Seahorse=%v",
			factory.sessionCalls,
			factory.seahorsePaths,
		)
	}
	if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
		t.Fatalf("rejected context manager created execution root: %v", statErr)
	}
	if _, statErr := os.Stat(stateRoot); !os.IsNotExist(statErr) {
		t.Fatalf("rejected context manager created state root: %v", statErr)
	}
}

func TestCodingRuntimeProfileRejectsHotReloadWithoutMutation(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	stateRoot := filepath.Join(root, "state")
	layout, err := NewCodingRuntimeLayout(
		"thread-reload",
		executionRoot,
		stateRoot,
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	profile, err := NewCodingRuntimeProfile(CodingRuntimeBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err != nil {
		t.Fatalf("NewCodingAgentLoop() error = %v", err)
	}
	t.Cleanup(loop.Close)
	originalRegistry := loop.GetRegistry()

	reloaded := *cfg
	if err := loop.ReloadProviderAndConfig(context.Background(), &mockProvider{}, &reloaded); err == nil {
		t.Fatal("ReloadProviderAndConfig() error = nil, want restart requirement")
	}
	if loop.GetRegistry() != originalRegistry {
		t.Fatal("rejected reload replaced the coding-profile registry")
	}
	agent := loop.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is nil after rejected reload")
	}
	if agent.CodingLayout.ThreadID() != layout.ThreadID() ||
		agent.CodingLayout.StateRoot() != layout.StateRoot() {
		t.Fatalf("layout after rejected reload = %#v, want %#v", agent.CodingLayout, layout)
	}
	if got := agent.Tools.List(); !slices.Equal(got, codingRuntimeToolNames) {
		t.Fatalf("coding tools after rejected reload = %v, want %v", got, codingRuntimeToolNames)
	}
	if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
		t.Fatalf("rejected reload created execution root: %v", statErr)
	}
}

func TestCodingRuntimeProfileKeepsMCPIsolatedWhenReloadRejected(t *testing.T) {
	root := t.TempDir()
	executionRoot := filepath.Join(root, "project")
	layout, err := NewCodingRuntimeLayout(
		"thread-mcp",
		executionRoot,
		filepath.Join(root, "state"),
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	profile, err := NewCodingRuntimeProfile(CodingRuntimeBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"must-not-start": {Enabled: true, Command: filepath.Join(root, "missing-mcp-server")},
	}
	loop, err := NewCodingAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, profile)
	if err != nil {
		t.Fatalf("NewCodingAgentLoop() error = %v", err)
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
		if got := agent.Tools.List(); !slices.Equal(got, codingRuntimeToolNames) {
			t.Fatalf("%s coding tools = %v, want %v", stage, got, codingRuntimeToolNames)
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
	layout, err := NewCodingRuntimeLayout(
		"thread-skills",
		executionRoot,
		stateRoot,
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout() error = %v", err)
	}
	profile, err := NewCodingRuntimeProfile(CodingRuntimeBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	loop, err := NewCodingAgentLoop(
		cfg,
		bus.NewMessageBus(),
		&mockProvider{},
		profile,
		WithIsolatedSkillBootstrap(),
	)
	if err != nil {
		t.Fatalf("NewCodingAgentLoop() error = %v", err)
	}
	t.Cleanup(loop.Close)
	if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
		t.Fatalf("isolated skill construction created execution root: %v", statErr)
	}
	if _, statErr := os.Stat(layout.StatePaths().MemoryRoot); statErr != nil {
		t.Fatalf("external memory root was not created: %v", statErr)
	}
	agent := loop.GetRegistry().GetDefaultAgent()
	messages := agent.ContextBuilder.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "inspect"})
	if len(messages) == 0 || !strings.Contains(messages[0].Content, codingAgentBaseInstructions) ||
		strings.Contains(messages[0].Content, "helpful AI assistant") {
		t.Fatalf("isolated skills changed coding prompt profile: %#v", messages)
	}

	reloaded := *cfg
	if err := loop.ReloadProviderAndConfig(context.Background(), &mockProvider{}, &reloaded); err == nil {
		t.Fatal("ReloadProviderAndConfig() error = nil, want restart requirement")
	}
	if _, statErr := os.Stat(executionRoot); !os.IsNotExist(statErr) {
		t.Fatalf("rejected isolated-skill reload created execution root: %v", statErr)
	}
}

func TestCodingPromptUsesRoutedLightCandidateIdentity(t *testing.T) {
	provider := &codingPromptAttemptProvider{}
	loop, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	agent.Model = "primary-name"
	agent.Candidates = []providers.FallbackCandidate{{
		Provider: "openai", Model: "primary-model", DisplayName: "primary-name",
	}}
	agent.LightCandidates = []providers.FallbackCandidate{{
		Provider: "anthropic", Model: "light-model", DisplayName: "light-name",
	}}
	agent.LightProvider = provider
	agent.Router = routing.New(routing.RouterConfig{LightModel: "light-name", Threshold: 1})
	configureCodingPromptTestAgent(agent, "thread-light")
	if err := loop.MountHook(NamedHook("json-round-trip", &llmJSONRoundTripUserAppendHook{})); err != nil {
		t.Fatalf("MountHook() error = %v", err)
	}

	opts := makeTestTurnSpec("coding:thread-light")
	opts.Dispatch.UserMessage = ""
	opts.CodingContext = codingPromptTestContext(agent, "thread-light")
	ts := newTurnState(agent, normalizeTurnSpec(opts), turnEventScope{
		turnID: "turn-light", context: newTurnContext(nil, nil, nil),
	})
	pipeline := newTestPipeline(loop)
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatalf("SetupTurn() error = %v", err)
	}
	if !exec.model.usedLight {
		t.Fatal("SetupTurn() did not select light route")
	}
	if _, err = pipeline.CallLLM(t.Context(), t.Context(), ts, exec, newLLMIterationState(1)); err != nil {
		t.Fatalf("CallLLM() error = %v", err)
	}
	assertCodingProviderIdentity(t, provider.Messages(), "light-name", "anthropic")
}

func TestCodingPromptUsesEachCrossProviderFallbackIdentity(t *testing.T) {
	primary := &codingPromptAttemptProvider{err: errors.New("status: 429 - rate limit exceeded")}
	fallback := &codingPromptAttemptProvider{}
	loop, agent, cleanup := newTurnCoordTestLoop(t, primary)
	defer cleanup()

	primaryCandidate := providers.FallbackCandidate{
		Provider: "openai", Model: "primary-model", DisplayName: "primary-name",
	}
	fallbackCandidate := providers.FallbackCandidate{
		Provider: "anthropic", Model: "fallback-model", DisplayName: "fallback-name",
	}
	agent.Model = "primary-name"
	agent.Candidates = []providers.FallbackCandidate{primaryCandidate, fallbackCandidate}
	agent.CandidateProviders = map[string]providers.LLMProvider{
		providers.ModelKey(fallbackCandidate.Provider, fallbackCandidate.Model): fallback,
	}
	configureCodingPromptTestAgent(agent, "thread-fallback")
	loop.fallback = providers.NewFallbackChain(providers.NewCooldownTracker(), nil)

	opts := makeTestTurnSpec("coding:thread-fallback")
	opts.CodingContext = codingPromptTestContext(agent, "thread-fallback")
	ts := newTurnState(agent, normalizeTurnSpec(opts), turnEventScope{
		turnID: "turn-fallback", context: newTurnContext(nil, nil, nil),
	})
	pipeline := newTestPipeline(loop)
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatalf("SetupTurn() error = %v", err)
	}
	if _, err = pipeline.CallLLM(t.Context(), t.Context(), ts, exec, newLLMIterationState(1)); err != nil {
		t.Fatalf("CallLLM() error = %v", err)
	}
	assertCodingProviderIdentity(t, primary.Messages(), "primary-name", "openai")
	assertCodingProviderIdentity(t, fallback.Messages(), "fallback-name", "anthropic")
}

func configureCodingPromptTestAgent(agent *AgentInstance, threadID string) {
	agent.ContextBuilder.codingPrompt = true
	agent.ContextBuilder.codingContext = codingPromptTestContext(agent, threadID)
	agent.ContextBuilder.InvalidateCache()
}

func codingPromptTestContext(agent *AgentInstance, threadID string) CodingPromptContext {
	return CodingPromptContext{
		ProjectRoot:      agent.Workspace,
		WorkingDirectory: agent.Workspace,
		ThreadID:         threadID,
		SessionKey:       "coding:" + threadID,
		TrustMode:        CodingTrustModeYolo,
	}
}

func assertCodingProviderIdentity(
	t *testing.T,
	messages []providers.Message,
	model string,
	provider string,
) {
	t.Helper()
	if len(messages) == 0 {
		t.Fatal("provider received no messages")
	}
	for _, want := range []string{"Model: " + model, "Provider: " + provider} {
		if !strings.Contains(messages[0].Content, want) {
			t.Fatalf("provider coding prompt missing %q:\n%s", want, messages[0].Content)
		}
	}
}

func TestNewCodingRuntimeProfileValidatesBindings(t *testing.T) {
	root := t.TempDir()
	if _, err := NewCodingRuntimeProfile(CodingRuntimeBinding{AgentID: "main"}); err == nil ||
		!strings.Contains(err.Error(), "thread ID") {
		t.Fatalf("NewCodingRuntimeProfile(empty layout) error = %v", err)
	}
	coding, err := NewCodingRuntimeLayout(
		"thread-1",
		filepath.Join(root, "coding-project"),
		filepath.Join(root, "coding-state"),
		[]string{filepath.Join(root, "coding-project")},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeLayout(coding) error = %v", err)
	}

	codingBinding := CodingRuntimeBinding{AgentID: "main", Layout: coding}
	if _, err = NewCodingRuntimeProfile(codingBinding); err != nil {
		t.Fatalf("NewCodingRuntimeProfile(coding) error = %v", err)
	}
	if _, err = NewCodingRuntimeProfile(codingBinding, codingBinding); err == nil {
		t.Fatal("NewCodingRuntimeProfile(duplicate) error = nil")
	}
	if _, err = NewCodingRuntimeProfile(
		codingBinding,
		CodingRuntimeBinding{AgentID: "support", Layout: coding},
	); err == nil || !strings.Contains(err.Error(), "thread \"thread-1\" is bound to agents") {
		t.Fatalf("NewCodingRuntimeProfile(shared thread) error = %v", err)
	}
}

func TestCodingRuntimeProfileRequiresExactConfiguredAgentSet(t *testing.T) {
	root := t.TempDir()
	newCodingLayout := func(id string) CodingRuntimeLayout {
		t.Helper()
		layout, err := NewCodingRuntimeLayout(
			"thread-"+id,
			filepath.Join(root, id+"-project"),
			filepath.Join(root, id+"-state"),
			[]string{filepath.Join(root, id+"-project")},
		)
		if err != nil {
			t.Fatalf("NewCodingRuntimeLayout(%s) error = %v", id, err)
		}
		return layout
	}
	profile, err := NewCodingRuntimeProfile(
		CodingRuntimeBinding{AgentID: "main", Layout: newCodingLayout("main")},
		CodingRuntimeBinding{AgentID: "support", Layout: newCodingLayout("support")},
	)
	if err != nil {
		t.Fatalf("NewCodingRuntimeProfile() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	if _, err = newAgentRegistryWithCodingRuntimeProfile(cfg, &mockProvider{}, profile); err == nil {
		t.Fatal("newAgentRegistryWithCodingRuntimeProfile(extra binding) error = nil")
	}
	cfg.Agents.List = []config.AgentConfig{{ID: "main"}, {ID: " MAIN "}}
	if _, err = newAgentRegistryWithCodingRuntimeProfile(cfg, &mockProvider{}, profile); err == nil {
		t.Fatal("newAgentRegistryWithCodingRuntimeProfile(duplicate configured ID) error = nil")
	}
}
