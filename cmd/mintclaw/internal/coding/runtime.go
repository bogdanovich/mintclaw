package coding

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal"
	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/coding/controller"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend/agentadapter"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

type codingTurnRequest struct {
	Store    *thread.Store
	Lease    *thread.Lease
	Metadata thread.Metadata
	Prompt   string
}

type codingTurnOutcome struct {
	Model        string
	Provider     string
	Response     string
	PromptStored bool
}

type codingTurnRunner interface {
	Run(context.Context, codingTurnRequest) (codingTurnOutcome, error)
}

type codingTurnRunnerFunc func(context.Context, codingTurnRequest) (codingTurnOutcome, error)

func (f codingTurnRunnerFunc) Run(
	ctx context.Context,
	request codingTurnRequest,
) (codingTurnOutcome, error) {
	return f(ctx, request)
}

type nativeCodingTurnRunner struct {
	loadConfig      func() (*config.Config, error)
	createProvider  func(*config.Config) (providers.LLMProvider, string, error)
	readTurnHistory func(context.Context, session.SessionStore, string) ([]providers.Message, error)
}

func newNativeCodingTurnRunner() codingTurnRunner {
	return newNativeCodingRuntimeDependencies()
}

func newNativeCodingRuntimeDependencies() nativeCodingTurnRunner {
	return nativeCodingTurnRunner{
		loadConfig:     internal.LoadConfig,
		createProvider: providers.CreateProvider,
		readTurnHistory: func(
			ctx context.Context,
			store session.SessionStore,
			sessionKey string,
		) ([]providers.Message, error) {
			return store.ReadTurnHistory(ctx, sessionKey)
		},
	}
}

func resolveNativeCodingModel(model string) (string, string, error) {
	cfg, err := internal.LoadConfig()
	if err != nil {
		return "", "", fmt.Errorf("coding runtime: load config: %w", err)
	}
	_, modelName, providerName, err := codingRuntimeConfig(cfg, thread.Metadata{Model: strings.TrimSpace(model)})
	return modelName, providerName, err
}

func (r nativeCodingTurnRunner) Run(
	ctx context.Context,
	request codingTurnRequest,
) (codingTurnOutcome, error) {
	runtime, err := openNativeCodingRuntime(r, request, nil, nil)
	if err != nil {
		return codingTurnOutcome{}, err
	}
	outcome, turnErr := runtime.runTurn(ctx, request.Prompt, nil)
	return outcome, errors.Join(turnErr, runtime.Close())
}

type nativeCodingRuntime struct {
	loop            *agent.AgentLoop
	messageBus      *bus.MessageBus
	eventBus        runtimeevents.Bus
	sessions        session.SessionStore
	readTurnHistory func(context.Context, session.SessionStore, string) ([]providers.Message, error)
	metadata        thread.Metadata
	model           string
	provider        string
	streaming       bool
	historyCursor   memory.HistoryCursor
	closeOnce       sync.Once
	closeErr        error
}

// codingCheckpointBus keeps durable coding-thread metadata observation in the
// coding composition root. Presentation adapters remain pure projections of
// the same lifecycle event.
type codingCheckpointBus struct {
	runtimeevents.Bus
	sessionKey string
	observe    func(agent.ContextCompressLifecyclePayload)
}

var _ runtimeevents.Bus = (*codingCheckpointBus)(nil)

func (b *codingCheckpointBus) Publish(
	ctx context.Context,
	event runtimeevents.Event,
) runtimeevents.PublishResult {
	b.observeEvent(event)
	return b.Bus.Publish(ctx, event)
}

func (b *codingCheckpointBus) PublishNonBlocking(event runtimeevents.Event) runtimeevents.PublishResult {
	b.observeEvent(event)
	return b.Bus.PublishNonBlocking(event)
}

func (b *codingCheckpointBus) observeEvent(event runtimeevents.Event) {
	if b == nil || b.observe == nil || event.Kind != runtimeevents.KindAgentContextCompressEnd ||
		event.Source.Component != "agent" ||
		(b.sessionKey != "" && event.Scope.SessionKey != b.sessionKey) {
		return
	}
	payload, ok := event.Payload.(agent.ContextCompressLifecyclePayload)
	if ok {
		b.observe(payload)
	}
}

const codingResumeRecoveryTimeout = 30 * time.Second

func openNativeCodingRuntime(
	r nativeCodingTurnRunner,
	request codingTurnRequest,
	projector *frontend.Projector,
	compactionObserver func(agent.ContextCompressLifecyclePayload),
) (*nativeCodingRuntime, error) {
	constructionCtx := context.Background()
	cancelConstruction := func() {}
	if projector != nil {
		constructionCtx, cancelConstruction = context.WithTimeout(constructionCtx, codingResumeRecoveryTimeout)
	}
	defer cancelConstruction()
	layout, err := runtimeLayoutFor(request.Store, request.Metadata)
	if err != nil {
		return nil, err
	}
	cfg, err := r.loadConfig()
	if err != nil {
		return nil, fmt.Errorf("coding runtime: load config: %w", err)
	}
	runtimeCfg, modelName, providerName, err := codingRuntimeConfig(cfg, request.Metadata)
	if err != nil {
		return nil, err
	}
	provider, _, err := r.createProvider(runtimeCfg)
	if err != nil {
		return nil, fmt.Errorf("coding runtime: create provider: %w", err)
	}
	profile, err := agent.NewCodingRuntimeProfile(agent.CodingRuntimeBinding{AgentID: "main", Layout: layout})
	if err != nil {
		return nil, err
	}
	messageBus := bus.NewMessageBus()
	baseEventBus := runtimeevents.NewBus()
	var eventBus runtimeevents.Bus = baseEventBus
	if projector != nil {
		eventBus, err = agentadapter.WrapBus(
			baseEventBus,
			projector,
			request.Metadata.SessionKey,
		)
		if err != nil {
			messageBus.Close()
			_ = baseEventBus.Close()
			return nil, err
		}
		messageBus.SetStreamDelegate(frontend.NewStreamDelegate(projector, request.Metadata.SessionKey))
	}
	if compactionObserver != nil {
		eventBus = &codingCheckpointBus{
			Bus:        eventBus,
			sessionKey: request.Metadata.SessionKey,
			observe:    compactionObserver,
		}
	}
	loop, err := agent.NewCodingAgentLoop(
		constructionCtx,
		runtimeCfg,
		messageBus,
		provider,
		profile,
		agent.WithRuntimeEvents(eventBus),
	)
	if err != nil {
		messageBus.Close()
		_ = baseEventBus.Close()
		return nil, fmt.Errorf("coding runtime: initialize agent: %w", err)
	}
	readTurnHistory := r.readTurnHistory
	if readTurnHistory == nil {
		readTurnHistory = func(
			readCtx context.Context,
			store session.SessionStore,
			sessionKey string,
		) ([]providers.Message, error) {
			return store.ReadTurnHistory(readCtx, sessionKey)
		}
	}
	runtime := &nativeCodingRuntime{
		loop:            loop,
		messageBus:      messageBus,
		eventBus:        baseEventBus,
		sessions:        loop.GetRegistry().GetDefaultAgent().Sessions,
		readTurnHistory: readTurnHistory,
		metadata:        request.Metadata,
		model:           modelName,
		provider:        providerName,
		streaming:       projector != nil,
	}
	if projector != nil {
		runtime.historyCursor, err = codingHistoryCursor(
			constructionCtx,
			runtime.sessions,
			request.Metadata.SessionKey,
		)
		if err != nil {
			_ = runtime.Close()
			return nil, fmt.Errorf("coding runtime: inspect transcript history: %w", err)
		}
	}
	return runtime, nil
}

func codingHistoryCursor(
	ctx context.Context,
	store session.SessionStore,
	sessionKey string,
) (memory.HistoryCursor, error) {
	if reader, ok := store.(session.TurnHistoryPageReader); ok {
		page, err := reader.ReadTurnHistoryPage(ctx, sessionKey, memory.HistoryPageRequest{Before: -1, Limit: 1})
		return page.Cursor, err
	}
	history, err := store.ReadTurnHistory(ctx, sessionKey)
	if err != nil {
		return memory.HistoryCursor{}, err
	}
	return memory.HistoryCursorForMessages(history, len(history))
}

func (r *nativeCodingRuntime) runTurn(
	ctx context.Context,
	prompt string,
	onReady func(),
) (codingTurnOutcome, error) {
	baseOutcome := codingTurnOutcome{Model: r.model, Provider: r.provider}
	beforeHistory, err := r.readTurnHistory(ctx, r.sessions, r.metadata.SessionKey)
	if err != nil {
		return baseOutcome, fmt.Errorf("coding runtime: read history before turn: %w", err)
	}
	response, turnErr := r.loop.ProcessDirectWithOptions(
		ctx,
		prompt,
		r.metadata.SessionKey,
		"coding",
		r.metadata.ThreadID,
		codingDirectTurnOptions(r.streaming, onReady),
	)
	after, historyErr := r.readTurnHistory(
		context.WithoutCancel(ctx),
		r.sessions,
		r.metadata.SessionKey,
	)
	promptStored := historyErr == nil && acceptedPromptAfter(after, len(beforeHistory), prompt)
	outcome := codingTurnOutcome{
		Model:        r.model,
		Provider:     r.provider,
		Response:     response,
		PromptStored: promptStored,
	}
	hardCanceled := errors.Is(context.Cause(ctx), controller.ErrHardCanceled)
	if historyErr != nil {
		return outcome, &thread.IndeterminatePromptError{
			ThreadID: r.metadata.ThreadID,
			Err: errors.Join(
				turnErr,
				fmt.Errorf("coding runtime: confirm history after turn: %w", historyErr),
			),
		}
	}
	if hardCanceled && !promptStored {
		return outcome, turnErr
	}
	if !promptStored {
		return outcome, &thread.IndeterminatePromptError{
			ThreadID: r.metadata.ThreadID,
			Err: errors.Join(
				turnErr,
				fmt.Errorf("coding runtime: confirmed history does not contain the admitted prompt"),
			),
		}
	}
	return outcome, turnErr
}

func codingDirectTurnOptions(streaming bool, onReady func()) agent.DirectTurnOptions {
	return agent.DirectTurnOptions{
		SuppressBackgroundCompaction: !streaming,
		EnableStreaming:              streaming,
		OnTurnReady:                  onReady,
	}
}

func (r *nativeCodingRuntime) Interrupt(_ context.Context) error {
	return r.loop.InterruptGracefulSession(r.metadata.SessionKey, "finish the current work and summarize")
}

func (r *nativeCodingRuntime) HardCancel(_ context.Context) error {
	return r.loop.HardAbort(r.metadata.SessionKey)
}

func (r *nativeCodingRuntime) Compact(ctx context.Context) error {
	return r.loop.CompactCodingSession(ctx, r.metadata.SessionKey)
}

func (r *nativeCodingRuntime) Close() error {
	r.closeOnce.Do(func() {
		r.closeErr = errors.Join(r.loop.CloseContext(context.Background()), r.eventBus.Close())
		r.messageBus.Close()
	})
	return r.closeErr
}

type codingMetadataState struct {
	mu       sync.Mutex
	metadata thread.Metadata
	store    *thread.Store
	now      func() time.Time
	save     func(thread.Metadata) error
	err      error
}

func newCodingMetadataState(
	store *thread.Store,
	metadata thread.Metadata,
	now func() time.Time,
) *codingMetadataState {
	if now == nil {
		now = time.Now
	}
	return &codingMetadataState{store: store, metadata: metadata, now: now}
}

func (s *codingMetadataState) update(
	mutate func(*thread.Metadata),
) (thread.Metadata, error) {
	return s.replace(func(metadata thread.Metadata) (thread.Metadata, error) {
		mutate(&metadata)
		return metadata, nil
	})
}

func (s *codingMetadataState) observeCompaction(payload agent.ContextCompressLifecyclePayload) {
	if s == nil || payload.Status != agent.ContextCompressLifecycleCompleted || payload.TranscriptRevision == 0 {
		return
	}
	completedAt := s.now().UTC()
	_, checkpointErr := s.update(func(metadata *thread.Metadata) {
		if completedAt.Before(metadata.CreatedAt) {
			completedAt = metadata.CreatedAt
		}
		metadata.Compaction = &thread.Compaction{
			At:       completedAt,
			Revision: payload.TranscriptRevision,
		}
		if metadata.UpdatedAt.Before(completedAt) {
			metadata.UpdatedAt = completedAt
		}
	})
	if checkpointErr != nil {
		s.mu.Lock()
		s.err = errors.Join(s.err, checkpointErr)
		s.mu.Unlock()
	}
}

func (s *codingMetadataState) recordTurn(
	title string,
	preview string,
	model string,
	provider string,
) (thread.Metadata, error) {
	return s.update(func(metadata *thread.Metadata) {
		if metadata.PendingFirstPrompt {
			metadata.Title = title
			metadata.PendingFirstPrompt = false
		}
		metadata.Preview = preview
		metadata.Model = model
		metadata.Provider = provider
		metadata.UpdatedAt = s.now().UTC()
	})
}

func (s *codingMetadataState) rename(title string) (thread.Metadata, error) {
	return s.replace(func(metadata thread.Metadata) (thread.Metadata, error) {
		return metadata.Rename(title, s.now())
	})
}

func (s *codingMetadataState) setArchived(archived bool) (thread.Metadata, error) {
	return s.replace(func(metadata thread.Metadata) (thread.Metadata, error) {
		return metadata.SetArchived(archived, s.now())
	})
}

func (s *codingMetadataState) replace(
	mutate func(thread.Metadata) (thread.Metadata, error),
) (thread.Metadata, error) {
	if s == nil {
		return thread.Metadata{}, fmt.Errorf("coding metadata state is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate, err := mutate(s.metadata)
	if err != nil {
		return s.metadata, err
	}
	save := s.save
	if save == nil && s.store != nil {
		save = s.store.Save
	}
	if save == nil {
		return s.metadata, fmt.Errorf("coding metadata store is unavailable")
	}
	if err = save(candidate); err != nil {
		if !fileutil.IsCommittedWriteError(err) {
			return s.metadata, err
		}
		s.err = errors.Join(s.err, err)
	}
	s.metadata = candidate
	return s.metadata, nil
}

func (s *codingMetadataState) accumulatedError() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

type nativeControllerRuntime struct {
	*nativeCodingRuntime
	lease         *thread.Lease
	projector     *frontend.Projector
	metadataState *codingMetadataState
}

var (
	_ controller.Runtime                    = (*nativeControllerRuntime)(nil)
	_ frontend.TranscriptPager              = (*nativeControllerRuntime)(nil)
	_ frontend.WorkspaceRefresher           = (*nativeControllerRuntime)(nil)
	_ frontend.ThreadLifecycle              = (*nativeControllerRuntime)(nil)
	_ frontend.BackgroundCompactionObserver = (*nativeControllerRuntime)(nil)
)

const hydratedTranscriptTextBytes = 32 << 10

func (r *nativeControllerRuntime) BackgroundCompactionActive() bool {
	return r.loop != nil && r.loop.CodingBackgroundCompactionActive(r.metadata.ThreadID)
}

func (r *nativeControllerRuntime) Rename(_ context.Context, title string) error {
	candidate, err := r.metadataState.rename(title)
	if err != nil {
		return err
	}
	return agentadapter.ProjectThreadMetadata(r.projector, candidate)
}

func (r *nativeControllerRuntime) SetArchived(_ context.Context, archived bool) error {
	candidate, err := r.metadataState.setArchived(archived)
	if err != nil {
		return err
	}
	return agentadapter.ProjectThreadMetadata(r.projector, candidate)
}

func (r *nativeControllerRuntime) RefreshWorkspace(ctx context.Context) error {
	if r.loop == nil || r.projector == nil {
		return frontend.ErrWorkspaceRefreshUnsupported
	}
	registry := r.loop.GetRegistry()
	if registry == nil {
		return frontend.ErrWorkspaceRefreshUnsupported
	}
	agentInstance := registry.GetDefaultAgent()
	if agentInstance == nil || agentInstance.ContextBuilder == nil {
		return frontend.ErrWorkspaceRefreshUnsupported
	}
	snapshot, changed := agentInstance.ContextBuilder.RefreshCodingWorkspace(ctx)
	if !changed {
		return nil
	}
	r.projector.WorkspaceUpdated(snapshot)
	return nil
}

func (r *nativeControllerRuntime) TranscriptPage(
	ctx context.Context,
	request frontend.TranscriptPageRequest,
) (frontend.TranscriptPage, error) {
	reader, ok := r.sessions.(session.TurnHistoryPageReader)
	if !ok {
		return frontend.TranscriptPage{}, fmt.Errorf("coding transcript paging is unavailable")
	}
	before := request.Before
	if before < 0 || before > r.historyCursor.Total {
		before = r.historyCursor.Total
	}
	page, err := reader.ReadTurnHistoryPage(ctx, r.metadata.SessionKey, memory.HistoryPageRequest{
		Before: before,
		Limit:  request.Limit,
		Cursor: &r.historyCursor,
	})
	if err != nil {
		if errors.Is(err, memory.ErrHistoryCursorStale) {
			return frontend.TranscriptPage{}, fmt.Errorf("%w: %w", frontend.ErrTranscriptHistoryChanged, err)
		}
		return frontend.TranscriptPage{}, err
	}
	entries := make([]frontend.TranscriptEntry, 0, len(page.Messages)*2)
	for offset, message := range page.Messages {
		entries = append(entries, hydratedTranscriptEntries(page.Start+offset, message)...)
	}
	end := min(page.End, r.historyCursor.Total)
	return frontend.TranscriptPage{
		Entries:  entries,
		Start:    page.Start,
		End:      end,
		Total:    r.historyCursor.Total,
		HasOlder: page.Start > 0,
		HasNewer: end < r.historyCursor.Total,
	}, nil
}

func hydratedTranscriptEntries(index int, message providers.Message) []frontend.TranscriptEntry {
	turnID := fmt.Sprintf("history-message-%d", index)
	entry := func(kind frontend.EntryKind, suffix string, text string) frontend.TranscriptEntry {
		text, truncated := boundHydratedTranscriptText(text)
		return frontend.TranscriptEntry{
			ID:        fmt.Sprintf("history:%d:%s", index, suffix),
			TurnID:    turnID,
			Kind:      kind,
			Text:      text,
			Complete:  true,
			Truncated: truncated,
		}
	}
	switch strings.ToLower(strings.TrimSpace(message.Role)) {
	case "user":
		if strings.TrimSpace(message.Content) != "" {
			return []frontend.TranscriptEntry{entry(frontend.EntryUser, "user", message.Content)}
		}
	case "assistant":
		entries := make([]frontend.TranscriptEntry, 0, 2)
		if strings.TrimSpace(message.ReasoningContent) != "" {
			entries = append(entries, entry(frontend.EntryReasoning, "reasoning", message.ReasoningContent))
		}
		if strings.TrimSpace(message.Content) != "" {
			entries = append(entries, entry(frontend.EntryAssistant, "assistant", message.Content))
		}
		return entries
	case "tool":
		switch message.ToolResultStatus {
		case providers.ToolResultStatusInterrupted:
			return []frontend.TranscriptEntry{
				entry(frontend.EntryTool, "tool-interrupted", "[interrupted] Prior tool execution was not run again."),
			}
		case providers.ToolResultStatusUnknown, providers.ToolResultStatusUnresolved:
			return []frontend.TranscriptEntry{
				entry(
					frontend.EntryTool,
					"tool-unknown",
					"[unknown] Prior tool outcome is unknown; inspect current state before retrying.",
				),
			}
		}
	}
	return nil
}

func boundHydratedTranscriptText(value string) (string, bool) {
	if len(value) <= hydratedTranscriptTextBytes {
		return value, false
	}
	value = value[:hydratedTranscriptTextBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value, true
}

func (r *nativeControllerRuntime) RunTurn(ctx context.Context, prompt string, onReady func()) error {
	outcome, turnErr := r.runTurn(ctx, prompt, onReady)
	return r.persistTurnOutcome(prompt, outcome, turnErr)
}

func (r *nativeControllerRuntime) persistTurnOutcome(
	prompt string,
	outcome codingTurnOutcome,
	turnErr error,
) error {
	if !outcome.PromptStored {
		return turnErr
	}
	title, preview, displayErr := thread.DisplayFromRequest(prompt)
	if displayErr != nil {
		return errors.Join(turnErr, displayErr)
	}
	candidate, saveErr := r.metadataState.recordTurn(title, preview, r.model, r.provider)
	projectionErr := agentadapter.ProjectThreadMetadata(r.projector, candidate)
	return errors.Join(turnErr, saveErr, projectionErr)
}

func (r *nativeControllerRuntime) Close() error {
	return errors.Join(
		r.nativeCodingRuntime.Close(),
		r.lease.Release(),
		r.metadataState.accumulatedError(),
	)
}

func newNativeCodingControllerWithDependencies(
	request codingTurnRequest,
	resumed bool,
	limits frontend.ProjectionLimits,
	dependencies nativeCodingTurnRunner,
	now func() time.Time,
) (frontend.Controller, error) {
	if request.Store == nil || request.Lease == nil {
		return nil, fmt.Errorf("coding controller requires a store and thread lease")
	}
	if err := request.Store.ValidateLease(request.Lease, request.Metadata.ThreadID); err != nil {
		return nil, fmt.Errorf("coding controller requires an active thread lease: %w", err)
	}
	projector, err := frontend.NewProjector(request.Metadata.ThreadID, limits)
	if err != nil {
		return nil, err
	}
	projector.Open(resumed)
	if projectionErr := agentadapter.ProjectThreadMetadata(projector, request.Metadata); projectionErr != nil {
		return nil, projectionErr
	}
	metadataState := newCodingMetadataState(request.Store, request.Metadata, now)
	native, err := openNativeCodingRuntime(
		dependencies,
		request,
		projector,
		metadataState.observeCompaction,
	)
	if err != nil {
		return nil, err
	}
	runtime := &nativeControllerRuntime{
		nativeCodingRuntime: native,
		lease:               request.Lease,
		projector:           projector,
		metadataState:       metadataState,
	}
	result, err := controller.New(projector, runtime)
	if err != nil {
		_ = runtime.Close()
		return nil, err
	}
	return result, nil
}

func newNativeCodingController(
	request codingTurnRequest,
	resumed bool,
) (frontend.Controller, error) {
	return newNativeCodingControllerWithDependencies(
		request,
		resumed,
		frontend.ProjectionLimits{},
		newNativeCodingRuntimeDependencies(),
		time.Now,
	)
}

func codingRuntimeConfig(
	cfg *config.Config,
	metadata thread.Metadata,
) (*config.Config, string, string, error) {
	if cfg == nil {
		return nil, "", "", fmt.Errorf("coding runtime: config is required")
	}
	runtimeCfg := *cfg
	runtimeCfg.Agents = cfg.Agents
	runtimeCfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	runtimeCfg.Agents.Dispatch = nil
	runtimeCfg.Agents.Defaults.Routing = nil
	runtimeCfg.Agents.Defaults.ModelFallbacks = nil
	// Coding continuation depends on budget-aware assembly of canonical history.
	// It always owns a disposable Seahorse index under this thread's StateRoot;
	// personal-agent context mode and custom database paths are not inherited.
	runtimeCfg.Agents.Defaults.ContextManager = "seahorse"
	runtimeCfg.Agents.Defaults.ContextManagerConfig = nil
	modelName := strings.TrimSpace(metadata.Model)
	if modelName == "" {
		modelName = strings.TrimSpace(runtimeCfg.Agents.Defaults.GetModelName())
	}
	if modelName == "" {
		return nil, "", "", fmt.Errorf("coding runtime: model is required")
	}
	persistedProvider := providers.NormalizeProvider(strings.TrimSpace(metadata.Provider))
	modelCfg, err := selectCodingModelConfig(cfg, modelName, persistedProvider)
	if err != nil {
		return nil, "", "", fmt.Errorf("coding runtime: select model %q: %w", modelName, err)
	}
	selected := cloneModelConfig(modelCfg)
	if persistedProvider != "" {
		_, canonicalModelID := providers.ExtractProtocol(selected)
		selected.Model = canonicalModelID
		selected.Provider = persistedProvider
	}
	runtimeCfg.Agents.Defaults.ModelName = modelName
	runtimeCfg.ModelList = config.SecureModelList{selected}
	for _, candidate := range cfg.ModelList {
		if candidate == nil || candidate.ModelName == modelName {
			continue
		}
		runtimeCfg.ModelList = append(runtimeCfg.ModelList, cloneModelConfig(candidate))
	}
	providerName, _ := providers.ExtractProtocol(selected)
	providerName = providers.NormalizeProvider(providerName)
	if providerName == "" {
		return nil, "", "", fmt.Errorf("coding runtime: provider is required for model %q", modelName)
	}
	runtimeCfg.Agents.Defaults.Provider = providerName
	return &runtimeCfg, modelName, providerName, nil
}

func selectCodingModelConfig(
	cfg *config.Config,
	modelName string,
	persistedProvider string,
) (*config.ModelConfig, error) {
	for _, candidate := range cfg.ModelList {
		if candidate == nil || candidate.ModelName != modelName ||
			!candidate.Enabled || candidate.IsVirtual() {
			continue
		}
		if persistedProvider == "" {
			return candidate, nil
		}
		providerName, _ := providers.ExtractProtocol(candidate)
		if providers.NormalizeProvider(providerName) == persistedProvider {
			return candidate, nil
		}
	}
	if persistedProvider == "" {
		return nil, fmt.Errorf("model not found in model_list")
	}
	return nil, fmt.Errorf("provider %q has no configured entry for this model alias", persistedProvider)
}

func cloneModelConfig(model *config.ModelConfig) *config.ModelConfig {
	if model == nil {
		return nil
	}
	cloned := *model
	cloned.APIKeys = append(config.SecureStrings(nil), model.APIKeys...)
	cloned.Fallbacks = append([]string(nil), model.Fallbacks...)
	if model.ExtraBody != nil {
		cloned.ExtraBody = make(map[string]any, len(model.ExtraBody))
		for key, value := range model.ExtraBody {
			cloned.ExtraBody[key] = value
		}
	}
	if model.CustomHeaders != nil {
		cloned.CustomHeaders = make(map[string]string, len(model.CustomHeaders))
		for key, value := range model.CustomHeaders {
			cloned.CustomHeaders[key] = value
		}
	}
	return &cloned
}

func acceptedPromptAfter(history []providers.Message, before int, prompt string) bool {
	if before < 0 || before > len(history) {
		before = 0
	}
	for _, message := range history[before:] {
		if message.Role == "user" && message.Content == prompt {
			return true
		}
	}
	return false
}
