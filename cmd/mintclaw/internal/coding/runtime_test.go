package coding

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/coding/controller"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend/agentadapter"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

type blockingCodingProvider struct {
	started chan struct{}
	once    sync.Once
}

func (p *blockingCodingProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "non-streaming fallback"}, nil
}

func (p *blockingCodingProvider) ChatStreamEvents(
	ctx context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
	onChunk func(providers.StreamChunk),
) (*providers.LLMResponse, error) {
	onChunk(providers.StreamChunk{Content: "working"})
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *blockingCodingProvider) GetDefaultModel() string { return "coding-test" }

func (p *blockingCodingProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{
		Streaming: true,
	}
}

func TestPendingThreadTitlePromotesOnceUnlessRenamed(t *testing.T) {
	project, err := thread.ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, time.August, 29, 17, 0, 0, 0, time.UTC)
	newState := func() *codingMetadataState {
		metadata, metadataErr := thread.NewPendingMetadata(thread.NewThreadID(), project, created)
		if metadataErr != nil {
			t.Fatal(metadataErr)
		}
		state := newCodingMetadataState(nil, metadata, func() time.Time {
			return created.Add(time.Minute)
		})
		state.save = func(thread.Metadata) error { return nil }
		return state
	}

	promoted, err := newState().recordTurn("Inspect repository", "Inspect repository carefully", "model", "provider")
	if err != nil {
		t.Fatal(err)
	}
	if promoted.PendingFirstPrompt || promoted.Title != "Inspect repository" ||
		promoted.Preview != "Inspect repository carefully" {
		t.Fatalf("promoted metadata = %+v", promoted)
	}

	renamedState := newState()
	renamed, err := renamedState.rename("Keep this title")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.PendingFirstPrompt {
		t.Fatalf("renamed metadata remained pending = %+v", renamed)
	}
	afterTurn, err := renamedState.recordTurn("Ignored generated title", "First prompt", "model", "provider")
	if err != nil {
		t.Fatal(err)
	}
	if afterTurn.Title != "Keep this title" || afterTurn.Preview != "First prompt" {
		t.Fatalf("renamed first turn metadata = %+v", afterTurn)
	}
}

func TestCodingRuntimeConfigIsolatesAgentContextAndSelection(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.Defaults.ContextManagerConfig = json.RawMessage(`{"dbPath":"/personal/context.db"}`)
	cfg.Agents.Defaults.ModelName = "default-model"
	cfg.Agents.Defaults.Provider = "personal-provider"
	cfg.Agents.Defaults.ModelFallbacks = []string{"personal-fallback"}
	cfg.Agents.Defaults.Routing = &config.RoutingConfig{
		Enabled:    true,
		LightModel: "personal-light-model",
	}
	cfg.Agents.List = []config.AgentConfig{{ID: "personal"}, {ID: "support"}}
	cfg.Agents.Dispatch = &config.DispatchConfig{}
	selected := &config.ModelConfig{
		ModelName: "coding-model",
		Provider:  "configured-provider",
		Model:     "configured-id",
		Enabled:   true,
		Fallbacks: []string{"fallback"},
	}
	fallback := &config.ModelConfig{
		ModelName: "fallback",
		Provider:  "fallback-provider",
		Model:     "fallback-id",
		Enabled:   true,
	}
	cfg.ModelList = config.SecureModelList{selected, fallback}

	runtimeCfg, modelName, providerName, err := codingRuntimeConfig(cfg, thread.Metadata{
		Model:    "coding-model",
		Provider: "configured-provider",
	})
	if err != nil {
		t.Fatal(err)
	}
	if modelName != "coding-model" || providerName != "configured-provider" {
		t.Fatalf("selection = model %q provider %q", modelName, providerName)
	}
	if runtimeCfg.Agents.Defaults.ContextManager != "seahorse" ||
		len(runtimeCfg.Agents.Defaults.ContextManagerConfig) != 0 {
		t.Fatalf(
			"coding context = %q config %s",
			runtimeCfg.Agents.Defaults.ContextManager,
			runtimeCfg.Agents.Defaults.ContextManagerConfig,
		)
	}
	if len(runtimeCfg.Agents.List) != 1 || runtimeCfg.Agents.List[0].ID != "main" ||
		runtimeCfg.Agents.Dispatch != nil || runtimeCfg.Agents.Defaults.Routing != nil ||
		len(runtimeCfg.Agents.Defaults.ModelFallbacks) != 0 {
		t.Fatalf("coding agents = %#v dispatch = %#v", runtimeCfg.Agents.List, runtimeCfg.Agents.Dispatch)
	}
	if len(runtimeCfg.ModelList) != 2 || runtimeCfg.ModelList[0].Provider != "configured-provider" {
		t.Fatalf("runtime models = %#v", runtimeCfg.ModelList)
	}
	if cfg.Agents.Defaults.ContextManager != "none" ||
		string(cfg.Agents.Defaults.ContextManagerConfig) != `{"dbPath":"/personal/context.db"}` ||
		cfg.Agents.Defaults.Routing == nil ||
		len(cfg.Agents.Defaults.ModelFallbacks) != 1 ||
		selected.Provider != "configured-provider" ||
		cfg.Agents.List[0].ID != "personal" {
		t.Fatalf("source config was mutated: %#v %#v", cfg.Agents, selected)
	}
	runtimeCfg.ModelList[0].Fallbacks[0] = "changed"
	if selected.Fallbacks[0] != "fallback" {
		t.Fatal("runtime model slice aliases the source model")
	}
}

func TestCodingRuntimeConfigPreservesCanonicalModelContract(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = config.SecureModelList{&config.ModelConfig{
		ModelName: "coding-model", Provider: "openai", Model: "gpt-4o",
		Enabled: true,
	}}

	runtimeCfg, modelName, providerName, err := codingRuntimeConfig(cfg, thread.Metadata{
		Model:    "coding-model",
		Provider: "openai",
	})
	if err != nil {
		t.Fatal(err)
	}
	if modelName != "coding-model" || providerName != "openai" {
		t.Fatalf("selection = model %q provider %q", modelName, providerName)
	}
	if got := runtimeCfg.ModelList[0].Model; got != "gpt-4o" {
		t.Fatalf("canonical runtime model = %q, want gpt-4o", got)
	}
	if cfg.ModelList[0].Model != "gpt-4o" {
		t.Fatalf("source model was mutated: %q", cfg.ModelList[0].Model)
	}
}

func TestCodingRuntimeConfigKeepsLoadBalancedAliasBoundToPersistedProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = config.SecureModelList{
		&config.ModelConfig{
			ModelName: "balanced",
			Provider:  "openai",
			Model:     "gpt-4o",
			APIBase:   "https://openai.example.test",
			Enabled:   true,
		},
		&config.ModelConfig{
			ModelName: "balanced",
			Provider:  "anthropic",
			Model:     "claude-sonnet",
			APIBase:   "https://anthropic.example.test",
			Enabled:   true,
		},
	}

	runtimeCfg, _, providerName, err := codingRuntimeConfig(cfg, thread.Metadata{
		Model:    "balanced",
		Provider: "anthropic",
	})
	if err != nil {
		t.Fatal(err)
	}
	selected := runtimeCfg.ModelList[0]
	if providerName != "anthropic" || selected.Provider != "anthropic" ||
		selected.Model != "claude-sonnet" || selected.APIBase != "https://anthropic.example.test" {
		t.Fatalf("selected mismatched alias entry: provider=%q config=%#v", providerName, selected)
	}
}

func TestCodingRuntimeConfigPinsSameProviderAliasToFirstConfiguredEntry(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = config.SecureModelList{
		&config.ModelConfig{
			ModelName: "balanced",
			Provider:  "openai",
			Model:     "gpt-first",
			APIBase:   "https://first.example.test",
			Enabled:   true,
		},
		&config.ModelConfig{
			ModelName: "balanced",
			Provider:  "openai",
			Model:     "gpt-second",
			APIBase:   "https://second.example.test",
			Enabled:   true,
		},
	}

	for _, metadata := range []thread.Metadata{
		{Model: "balanced"},
		{Model: "balanced", Provider: "openai"},
	} {
		for attempt := 0; attempt < 4; attempt++ {
			runtimeCfg, _, providerName, err := codingRuntimeConfig(cfg, metadata)
			if err != nil {
				t.Fatal(err)
			}
			selected := runtimeCfg.ModelList[0]
			if providerName != "openai" || selected.Model != "gpt-first" ||
				selected.APIBase != "https://first.example.test" {
				t.Fatalf("attempt %d reconstructed %#v with provider %q", attempt, selected, providerName)
			}
		}
	}
}

func TestCodingRuntimeConfigSkipsDisabledAliasEntries(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = config.SecureModelList{
		&config.ModelConfig{
			ModelName: "coding-model",
			Provider:  "openai",
			Model:     "disabled-model",
			APIKeys:   config.SimpleSecureStrings("disabled-key"),
		},
		&config.ModelConfig{
			ModelName: "coding-model",
			Provider:  "anthropic",
			Model:     "enabled-model",
			Enabled:   true,
		},
	}

	runtimeCfg, _, providerName, err := codingRuntimeConfig(cfg, thread.Metadata{Model: "coding-model"})
	if err != nil {
		t.Fatal(err)
	}
	selected := runtimeCfg.ModelList[0]
	if providerName != "anthropic" || selected.Model != "enabled-model" {
		t.Fatalf("selected disabled alias entry: provider=%q config=%#v", providerName, selected)
	}

	_, _, _, err = codingRuntimeConfig(cfg, thread.Metadata{
		Model:    "coding-model",
		Provider: "openai",
	})
	if err == nil {
		t.Fatal("disabled persisted provider selection unexpectedly succeeded")
	}
}

func TestNativeControllerDrivesAndInterruptsHeadlessCodingTurn(t *testing.T) {
	project, err := thread.ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := thread.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := thread.NewMetadata(thread.NewThreadID(), project, "initial", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	metadata.Model = "coding-test"
	metadata.Provider = "openai"
	if err := store.ProvisionThread(metadata.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := codingworkspace.NewRepository(
		project.ProjectRoot,
		project.InvocationCWD,
		codingworkspace.Limits{},
	).CaptureBaseline(t.Context(), codingworkspace.BaselineRequest{
		ProjectKey: project.ProjectKey,
		CapturedAt: time.Now().UTC(),
	})
	if err != nil {
		_ = lease.Release()
		t.Fatal(err)
	}
	if err := store.PublishRepositoryBaseline(t.Context(), lease, metadata, baseline); err != nil {
		_ = lease.Release()
		t.Fatal(err)
	}
	provider := &blockingCodingProvider{started: make(chan struct{})}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = metadata.Model
	cfg.Agents.Defaults.Provider = metadata.Provider
	cfg.Agents.Defaults.MaxTokens = 256
	cfg.Agents.Defaults.ContextWindow = 32_000
	cfg.ModelList = config.SecureModelList{&config.ModelConfig{
		ModelName: metadata.Model,
		Provider:  metadata.Provider,
		Model:     "test-model-id",
		Enabled:   true,
		Streaming: config.ModelStreamingConfig{Enabled: true},
	}}
	dependencies := nativeCodingTurnRunner{
		loadConfig: func() (*config.Config, error) { return cfg, nil },
		createProvider: func(*config.Config) (providers.LLMProvider, string, error) {
			return provider, metadata.Model, nil
		},
	}
	frontendController, err := newNativeCodingControllerWithDependencies(
		codingTurnRequest{Store: store, Lease: lease, Metadata: metadata},
		true,
		frontend.ProjectionLimits{},
		dependencies,
		time.Now,
	)
	if err != nil {
		_ = lease.Release()
		t.Fatal(err)
	}
	refresher, ok := frontendController.(frontend.WorkspaceRefresher)
	if !ok {
		t.Fatal("native coding controller does not expose workspace refresh")
	}
	subscribeCtx, cancelSubscribe := context.WithCancel(t.Context())
	defer cancelSubscribe()
	_, workspaceUpdates, err := frontendController.Subscribe(subscribeCtx)
	if err != nil {
		t.Fatal(err)
	}
	if err := refresher.RefreshWorkspace(t.Context()); err != nil {
		t.Fatalf("RefreshWorkspace() error = %v", err)
	}
	select {
	case update := <-workspaceUpdates:
		if update.Workspace == nil || update.RepositoryStatus == nil {
			t.Fatalf("workspace refresh update is not atomic = %#v", update)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workspace refresh did not publish an update")
	}
	select {
	case update := <-workspaceUpdates:
		t.Fatalf("workspace refresh published an intermediate update = %#v", update)
	case <-time.After(100 * time.Millisecond):
	}
	refreshed, err := frontendController.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Workspace == nil || refreshed.Workspace.ProjectRoot != project.ProjectRoot ||
		refreshed.Workspace.CWD != project.InvocationCWD {
		t.Fatalf("refreshed workspace = %+v", refreshed.Workspace)
	}
	if refreshed.RepositoryStatus == nil || refreshed.RepositoryStatus.BaselineID != baseline.BaselineID {
		t.Fatalf("refreshed repository status = %+v", refreshed.RepositoryStatus)
	}
	evidence, ok := frontendController.(frontend.RepositoryEvidenceReader)
	if !ok {
		t.Fatal("native coding controller does not expose repository evidence")
	}
	diff, err := evidence.RepositoryDiff(
		t.Context(),
		codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
	)
	if err != nil || diff.BaselineID != baseline.BaselineID || diff.Target.Kind != codingworkspace.DiffTargetCurrent {
		t.Fatalf("current repository diff with baseline provenance = %+v / %v", diff, err)
	}
	if err := frontendController.Submit(
		t.Context(),
		frontend.TurnInput{Text: "inspect the project"},
	); err != nil {
		t.Fatal(err)
	}
	if err := frontendController.Interrupt(t.Context()); err != nil {
		t.Fatalf("immediate native Interrupt() error = %v", err)
	}
	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not receive the headless turn")
	}
	interruptDeadline := time.Now().Add(5 * time.Second)
	var snapshot frontend.ThreadSnapshot
	for {
		snapshot, err = frontendController.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Activity == frontend.ActivityInterrupting {
			break
		}
		if time.Now().After(interruptDeadline) {
			t.Fatalf("activity = %q, want interrupting", snapshot.Activity)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(snapshot.Entries) < 2 || snapshot.Entries[len(snapshot.Entries)-1].Text != "working" {
		t.Fatalf("streamed entries = %#v", snapshot.Entries)
	}
	if err := frontendController.Submit(
		t.Context(),
		frontend.TurnInput{Text: "must remain in composer"},
	); !errors.Is(
		err,
		controller.ErrTurnActive,
	) {
		t.Fatalf("second Submit() error = %v, want %v", err, controller.ErrTurnActive)
	}
	if err := frontendController.HardCancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, err = frontendController.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Activity != frontend.ActivityRunning && snapshot.Activity != frontend.ActivityInterrupting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn did not stop: %#v", snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for {
		err = frontendController.Compact(t.Context())
		if err == nil {
			break
		}
		if !errors.Is(err, controller.ErrTurnActive) || time.Now().After(deadline) {
			t.Fatalf("Compact() after interruption error = %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for {
		snapshot, err = frontendController.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.LastCompaction != nil && snapshot.LastCompaction.Reason == "manual" &&
			snapshot.LastCompaction.Status != frontend.CompactionRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("manual compaction did not finish: %#v", snapshot.LastCompaction)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot.LastCompaction.Background {
		t.Fatal("manual compaction was projected as background work")
	}
	for _, entry := range snapshot.Entries {
		if entry.ID == "controller:turn-error" {
			t.Fatalf("intentional interruption was projected as a controller failure: %#v", entry)
		}
	}
	if err := frontendController.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	reacquired, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatalf("controller did not release thread lease: %v", err)
	}
	if err := reacquired.Release(); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Load(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Preview != metadata.Preview {
		t.Fatalf("canceled prompt changed preview to %q", persisted.Preview)
	}
}

func TestNativeControllerDoesNotReusePriorOutcomeAfterPreTurnFailure(t *testing.T) {
	project, err := thread.ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := thread.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := thread.NewMetadata(thread.NewThreadID(), project, "initial", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	metadata.Model = "coding-test"
	metadata.Provider = "openai"
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	projector, err := frontend.NewProjector(metadata.ThreadID, frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected pre-turn history failure")
	runtime := &nativeControllerRuntime{
		nativeCodingRuntime: &nativeCodingRuntime{
			metadata: metadata,
			model:    metadata.Model,
			provider: metadata.Provider,
			readTurnHistory: func(context.Context, session.SessionStore, string) ([]providers.Message, error) {
				return nil, injected
			},
		},
		projector:     projector,
		metadataState: newCodingMetadataState(store, metadata, time.Now),
	}
	if err := runtime.persistTurnOutcome("first stored prompt", codingTurnOutcome{
		Model: metadata.Model, Provider: metadata.Provider, PromptStored: true,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := runtime.RunTurn(
		t.Context(),
		frontend.TurnInput{Text: "second unstored prompt"},
		func() {},
	); !errors.Is(err, injected) {
		t.Fatalf("second RunTurn() error = %v, want %v", err, injected)
	}
	persisted, err := store.Load(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Preview != "first stored prompt" {
		t.Fatalf("preview = %q, want prior stored prompt", persisted.Preview)
	}
}

func TestCodingDirectTurnOptionsEnableBackgroundCompactionForPersistentRuntime(t *testing.T) {
	persistent := codingDirectTurnOptions(true, nil)
	if persistent.SuppressBackgroundCompaction || !persistent.EnableStreaming {
		t.Fatalf("persistent coding options = %+v", persistent)
	}
	shortLived := codingDirectTurnOptions(false, nil)
	if !shortLived.SuppressBackgroundCompaction || shortLived.EnableStreaming {
		t.Fatalf("short-lived coding options = %+v", shortLived)
	}
}

func TestNativeRuntimeAdmitsAttachmentAndStoresExactReference(t *testing.T) {
	store, lease, metadata := newRuntimeAttachmentThread(t)
	sessions := session.NewMemoryStore()
	source := filepath.Join(t.TempDir(), "screen[1].png")
	if err := os.WriteFile(source, []byte("verified image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := &nativeCodingRuntime{
		store:           store,
		lease:           lease,
		metadata:        metadata,
		sessions:        sessions,
		readTurnHistory: defaultRuntimeHistoryReader,
		now: func() time.Time {
			return time.Date(2026, time.August, 29, 22, 0, 0, 0, time.UTC)
		},
		processDirect: func(
			ctx context.Context,
			input agent.DirectTurnInput,
			sessionKey, _, _ string,
			_ agent.DirectTurnOptions,
		) (string, error) {
			if err := sessions.AppendTurnMessage(ctx, sessionKey, providers.Message{
				Role: "user", Content: input.Content, Media: input.Media, RootTurnStart: true,
			}); err != nil {
				return "", err
			}
			return "inspected", nil
		},
	}
	outcome, err := runtime.runTurn(t.Context(), frontend.TurnInput{
		Text: "What is wrong here?",
		Attachments: []frontend.TurnAttachment{{
			Path: source, Filename: "screen[1].png", ContentType: "image/png",
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.PromptStored || outcome.Response != "inspected" {
		t.Fatalf("outcome = %+v", outcome)
	}
	history, err := sessions.ReadTurnHistory(t.Context(), metadata.SessionKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Content != "[image: screen‹1›.png]\nWhat is wrong here?" ||
		len(history[0].Media) != 1 || !thread.IsAttachmentRef(history[0].Media[0]) {
		t.Fatalf("stored history = %+v", history)
	}
	attachments, err := store.ListAttachments(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].Ref != history[0].Media[0] {
		t.Fatalf("manifest = %+v, history = %+v", attachments, history)
	}
}

func TestNativeRuntimeRollsBackAttachmentWhenPromptWasNotStored(t *testing.T) {
	store, lease, metadata := newRuntimeAttachmentThread(t)
	sessions := session.NewMemoryStore()
	source := filepath.Join(t.TempDir(), "build.log")
	if err := os.WriteFile(source, []byte("compiler output"), 0o600); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected processor failure")
	runtime := &nativeCodingRuntime{
		store: store, lease: lease, metadata: metadata, sessions: sessions,
		readTurnHistory: defaultRuntimeHistoryReader,
		processDirect: func(
			context.Context,
			agent.DirectTurnInput,
			string,
			string,
			string,
			agent.DirectTurnOptions,
		) (string, error) {
			return "", injected
		},
	}
	outcome, err := runtime.runTurn(t.Context(), frontend.TurnInput{
		Attachments: []frontend.TurnAttachment{{Path: source, ContentType: "text/plain"}},
	}, nil)
	if !errors.Is(err, injected) || outcome.PromptStored {
		t.Fatalf("runTurn() outcome = %+v, error = %v", outcome, err)
	}
	var indeterminate *thread.IndeterminatePromptError
	if !errors.As(err, &indeterminate) {
		t.Fatalf("runTurn() error = %v, want indeterminate prompt classification", err)
	}
	attachments, listErr := store.ListAttachments(metadata.ThreadID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(attachments) != 0 {
		t.Fatalf("rolled-back manifest = %+v, want no references", attachments)
	}
}

func TestNativeRuntimeRollsBackAttachmentWhenCanonicalPromptExceedsBound(t *testing.T) {
	store, lease, metadata := newRuntimeAttachmentThread(t)
	source := filepath.Join(t.TempDir(), "build.log")
	if err := os.WriteFile(source, []byte("compiler output"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := &nativeCodingRuntime{store: store, lease: lease, metadata: metadata}
	_, attachments, err := runtime.admitTurnInput(t.Context(), frontend.TurnInput{
		Text: strings.Repeat("x", thread.MaxPromptBytes),
		Attachments: []frontend.TurnAttachment{{
			Path: source, ContentType: "text/plain",
		}},
	})
	if err == nil || len(attachments) != 0 {
		t.Fatalf("admitTurnInput() attachments = %+v, error = %v", attachments, err)
	}
	entries, listErr := store.ListAttachments(metadata.ThreadID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(entries) != 0 {
		t.Fatalf("oversized canonical prompt left refs = %+v", entries)
	}
}

func newRuntimeAttachmentThread(t *testing.T) (*thread.Store, *thread.Lease, thread.Metadata) {
	t.Helper()
	project, err := thread.ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := thread.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := thread.NewMetadata(thread.NewThreadID(), project, "attachments", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionThread(metadata.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	return store, lease, metadata
}

func defaultRuntimeHistoryReader(
	ctx context.Context,
	store session.SessionStore,
	sessionKey string,
) ([]providers.Message, error) {
	return store.ReadTurnHistory(ctx, sessionKey)
}

func TestCodingHistoryCursorHonorsRecoveryContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := codingHistoryCursor(ctx, session.NewMemoryStore(), "coding:thread")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("codingHistoryCursor() error = %v, want canceled", err)
	}
}

func TestNativeControllerPublishesOnlyCommittedMetadata(t *testing.T) {
	project, err := thread.ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := thread.NewMetadata(thread.NewThreadID(), project, "stored preview", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	metadata.Model = "coding-test"
	metadata.Provider = "openai"
	projector, err := frontend.NewProjector(metadata.ThreadID, frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := agentadapter.ProjectThreadMetadata(projector, metadata); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected pre-commit save failure")
	metadataState := newCodingMetadataState(
		nil,
		metadata,
		func() time.Time { return metadata.UpdatedAt.Add(time.Minute) },
	)
	metadataState.save = func(thread.Metadata) error { return injected }
	runtime := &nativeControllerRuntime{
		nativeCodingRuntime: &nativeCodingRuntime{
			metadata: metadata, model: metadata.Model, provider: metadata.Provider,
		},
		projector:     projector,
		metadataState: metadataState,
	}
	outcome := codingTurnOutcome{Model: metadata.Model, Provider: metadata.Provider, PromptStored: true}
	if err := runtime.persistTurnOutcome("unstored preview", outcome, nil); !errors.Is(err, injected) {
		t.Fatalf("persistTurnOutcome() error = %v, want %v", err, injected)
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if metadataState.metadata.Preview != metadata.Preview || snapshot.Metadata.Preview != metadata.Preview {
		t.Fatalf(
			"failed save leaked metadata: runtime=%q snapshot=%q",
			metadataState.metadata.Preview,
			snapshot.Metadata.Preview,
		)
	}
	committedCause := errors.New("injected post-rename sync failure")
	metadataState.save = func(thread.Metadata) error {
		return &fileutil.CommittedWriteError{Err: committedCause}
	}
	if err := runtime.persistTurnOutcome("committed preview", outcome, nil); err != nil {
		t.Fatalf("committed persistTurnOutcome() error = %v, want deferred warning", err)
	}
	snapshot, err = projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if metadataState.metadata.Preview != "committed preview" || snapshot.Metadata.Preview != "committed preview" {
		t.Fatalf(
			"committed metadata was not retained: runtime=%q snapshot=%q",
			metadataState.metadata.Preview,
			snapshot.Metadata.Preview,
		)
	}
	if !errors.Is(metadataState.accumulatedError(), committedCause) {
		t.Fatalf("deferred metadata warning = %v, want %v", metadataState.accumulatedError(), committedCause)
	}
}

func TestNativeControllerLifecyclePersistsAndProjectsAtomically(t *testing.T) {
	project, err := thread.ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	metadata, err := thread.NewMetadata(thread.NewThreadID(), project, "initial title", created)
	if err != nil {
		t.Fatal(err)
	}
	store, err := thread.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	projector, err := frontend.NewProjector(metadata.ThreadID, frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := agentadapter.ProjectThreadMetadata(projector, metadata); err != nil {
		t.Fatal(err)
	}
	now := created.Add(time.Minute)
	state := newCodingMetadataState(store, metadata, func() time.Time { return now })
	runtime := &nativeControllerRuntime{
		nativeCodingRuntime: &nativeCodingRuntime{metadata: metadata},
		projector:           projector,
		metadataState:       state,
	}
	if err := runtime.Rename(t.Context(), "renamed thread"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := runtime.SetArchived(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Load(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Title != "renamed thread" || stored.Status != thread.StatusArchived ||
		snapshot.Metadata.Title != stored.Title || !snapshot.Metadata.Archived {
		t.Fatalf("stored = %+v snapshot = %+v", stored, snapshot.Metadata)
	}
}

func TestNativeControllerTranscriptPageHydratesOnlySafeDisplayContent(t *testing.T) {
	sessions := session.NewMemoryStore()
	sessions.AddFullMessage("coding:thread", providers.Message{Role: "user", Content: "inspect 界"})
	sessions.AddFullMessage("coding:thread", providers.Message{
		Role: "assistant", ReasoningContent: "consider e\u0301", Content: "done 👩🏽‍💻",
	})
	sessions.AddFullMessage("coding:thread", providers.Message{Role: "tool", Content: "DO-NOT-HYDRATE-SECRET"})
	sessions.AddFullMessage("coding:thread", providers.Message{Role: "system", Content: "SYSTEM-SECRET"})
	opening, err := sessions.ReadTurnHistoryPage(
		t.Context(),
		"coding:thread",
		memory.HistoryPageRequest{Before: -1, Limit: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &nativeControllerRuntime{nativeCodingRuntime: &nativeCodingRuntime{
		sessions:      sessions,
		metadata:      thread.Metadata{SessionKey: "coding:thread"},
		historyCursor: opening.Cursor,
	}}
	page, err := runtime.TranscriptPage(t.Context(), frontend.TranscriptPageRequest{Before: -1, Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if page.Start != 0 || page.End != 4 || page.Total != 4 || page.HasOlder || page.HasNewer {
		t.Fatalf("page metadata = %+v", page)
	}
	if len(page.Entries) != 3 {
		t.Fatalf("safe entries = %+v", page.Entries)
	}
	if page.Entries[0].Kind != frontend.EntryUser || page.Entries[1].Kind != frontend.EntryReasoning ||
		page.Entries[2].Kind != frontend.EntryAssistant {
		t.Fatalf("entry kinds = %+v", page.Entries)
	}
	for _, entry := range page.Entries {
		if strings.Contains(entry.Text, "SECRET") {
			t.Fatalf("non-display history leaked through hydration: %+v", entry)
		}
	}

	sessions.AddFullMessage("coding:thread", providers.Message{Role: "assistant", Content: "post-open"})
	page, err = runtime.TranscriptPage(t.Context(), frontend.TranscriptPageRequest{Before: -1, Limit: 4})
	if err != nil || page.Total != 4 {
		t.Fatalf("append changed opening prefix page: page=%+v err=%v", page, err)
	}
	if err := sessions.ReplaceTurnHistory(t.Context(), "coding:thread", []providers.Message{
		{Role: "user", Content: "replacement"},
		{Role: "assistant", Content: "replacement answer"},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = runtime.TranscriptPage(t.Context(), frontend.TranscriptPageRequest{Before: -1, Limit: 4})
	if !errors.Is(err, frontend.ErrTranscriptHistoryChanged) {
		t.Fatalf("replacement page error = %v", err)
	}
}

func TestCodingMetadataStatePersistsOnlyCompletedCompactionCheckpoint(t *testing.T) {
	project, err := thread.ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := thread.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	metadata, err := thread.NewMetadata(thread.NewThreadID(), project, "compact safely", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	completedAt := createdAt.Add(time.Minute)
	state := newCodingMetadataState(store, metadata, func() time.Time { return completedAt })
	state.observeCompaction(agent.ContextCompressLifecyclePayload{
		Status: agent.ContextCompressLifecycleNoProgress, TranscriptRevision: 6,
	})
	unchanged, err := store.Load(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Compaction != nil {
		t.Fatalf("no-progress checkpoint = %+v, want nil", unchanged.Compaction)
	}
	state.observeCompaction(agent.ContextCompressLifecyclePayload{
		Status: agent.ContextCompressLifecycleCompleted, TranscriptRevision: 7,
	})
	persisted, err := store.Load(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Compaction == nil || persisted.Compaction.Revision != 7 ||
		!persisted.Compaction.At.Equal(completedAt) || !persisted.UpdatedAt.Equal(completedAt) {
		t.Fatalf("completed checkpoint = %+v updated=%s", persisted.Compaction, persisted.UpdatedAt)
	}
}

func TestCodingCheckpointBusObservesMatchingTerminalCompaction(t *testing.T) {
	t.Parallel()

	var observed []agent.ContextCompressLifecyclePayload
	checkpointBus := &codingCheckpointBus{
		Bus:        runtimeevents.NewBus(),
		sessionKey: "coding:thread",
		observe: func(payload agent.ContextCompressLifecyclePayload) {
			observed = append(observed, payload)
		},
	}
	t.Cleanup(func() { _ = checkpointBus.Close() })

	publish := func(kind runtimeevents.Kind, component, sessionKey string) {
		t.Helper()
		checkpointBus.PublishNonBlocking(runtimeevents.Event{
			Kind:   kind,
			Source: runtimeevents.Source{Component: component},
			Scope:  runtimeevents.Scope{SessionKey: sessionKey},
			Payload: agent.ContextCompressLifecyclePayload{
				Status:             agent.ContextCompressLifecycleCompleted,
				TranscriptRevision: 7,
			},
		})
	}

	publish(runtimeevents.KindAgentContextCompressProgress, "agent", "coding:thread")
	publish(runtimeevents.KindAgentContextCompressEnd, "agent", "coding:other")
	publish(runtimeevents.KindAgentContextCompressEnd, "gateway", "coding:thread")
	publish(runtimeevents.KindAgentContextCompressEnd, "agent", "coding:thread")

	if len(observed) != 1 || observed[0].TranscriptRevision != 7 {
		t.Fatalf("observed compactions = %+v, want one matching terminal payload", observed)
	}
}

func TestCodingMetadataStateKeepsCompactionDerivedWhenMetadataWriteFails(t *testing.T) {
	project, err := thread.ResolveProject(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := thread.NewMetadata(thread.NewThreadID(), project, "compact safely", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected metadata checkpoint failure")
	state := newCodingMetadataState(nil, metadata, time.Now)
	state.save = func(thread.Metadata) error { return injected }
	state.observeCompaction(agent.ContextCompressLifecyclePayload{
		Status: agent.ContextCompressLifecycleCompleted, TranscriptRevision: 9,
	})
	if !errors.Is(state.accumulatedError(), injected) {
		t.Fatalf("checkpoint error = %v, want %v", state.accumulatedError(), injected)
	}
	if state.metadata.Compaction != nil {
		t.Fatalf("failed metadata write became authoritative: %+v", state.metadata.Compaction)
	}
}

func TestHydratedTranscriptEntriesShowRecoveredToolUncertaintyWithoutOutput(t *testing.T) {
	for _, test := range []struct {
		status providers.ToolResultStatus
		want   string
	}{
		{status: providers.ToolResultStatusInterrupted, want: "[interrupted]"},
		{status: providers.ToolResultStatusUnknown, want: "[unknown]"},
		{status: providers.ToolResultStatusUnresolved, want: "[unknown]"},
	} {
		entries := hydratedTranscriptEntries(4, providers.Message{
			Role:             "tool",
			Content:          "SECRET TOOL OUTPUT",
			ToolCallID:       "private-call-id",
			ToolResultStatus: test.status,
		})
		if len(entries) != 1 || entries[0].Kind != frontend.EntryTool ||
			!strings.Contains(entries[0].Text, test.want) {
			t.Fatalf("status %q entries = %+v", test.status, entries)
		}
		if strings.Contains(entries[0].Text, "SECRET") || strings.Contains(entries[0].Text, "private-call-id") {
			t.Fatalf("recovery entry leaked canonical tool data: %+v", entries[0])
		}
	}
}

func TestHydratedTranscriptTextUsesBoundedValidUTF8(t *testing.T) {
	input := strings.Repeat("界", hydratedTranscriptTextBytes)
	text, truncated := boundHydratedTranscriptText(input)
	if !truncated || len(text) > hydratedTranscriptTextBytes || !utf8.ValidString(text) {
		t.Fatalf("bounded text bytes=%d truncated=%v valid=%v", len(text), truncated, utf8.ValidString(text))
	}
}
