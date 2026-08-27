//go:build !mipsle && !netbsd && !(freebsd && arm)

package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/seahorse"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

type clearFailingSessionStore struct {
	session.SessionStore
	err error
}

type staticHistorySessionStore struct {
	session.SessionStore
	history  []providers.Message
	revision memory.HistoryRevision
}

type advancingRevisionSessionStore struct {
	*staticHistorySessionStore
	mu    sync.Mutex
	calls int
}

func (s *advancingRevisionSessionStore) GetHistoryRevision(
	context.Context,
	string,
) (memory.HistoryRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	revision := s.revision
	if s.calls > 1 {
		revision.Revision++
		revision.Count++
	}
	return revision, nil
}

func (s *advancingRevisionSessionStore) GetHistory(string) []providers.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	history := append([]providers.Message(nil), s.history...)
	if s.calls > 1 {
		history = append(history, providers.Message{Role: "assistant", Content: "new canonical message"})
	}
	return history
}

func (s *advancingRevisionSessionStore) ReadTurnHistory(
	context.Context,
	string,
) ([]providers.Message, error) {
	return s.GetHistory(""), nil
}

func (s *staticHistorySessionStore) GetHistory(string) []providers.Message {
	return append([]providers.Message(nil), s.history...)
}

func (s *staticHistorySessionStore) ReadTurnHistory(context.Context, string) ([]providers.Message, error) {
	return s.GetHistory(""), nil
}

func (s *staticHistorySessionStore) GetHistoryRevision(
	context.Context,
	string,
) (memory.HistoryRevision, error) {
	return s.revision, nil
}

func (s *clearFailingSessionStore) ClearSession(context.Context, string) error {
	return s.err
}

func TestCanonicalHistoryContainsHandlesMissingStore(t *testing.T) {
	if canonicalHistoryContains(t.Context(), nil, "missing", providers.Message{}) {
		t.Fatal("missing canonical store reported a matching message")
	}
}

func newReconciliationTestManager(t *testing.T) (*seahorseContextManager, *memory.JSONLStore) {
	t.Helper()
	dir := t.TempDir()
	canonical, storeErr := memory.NewJSONLStore(dir + "/sessions")
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	engine, err := seahorse.NewEngine(seahorse.Config{DBPath: dir + "/seahorse.db"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return newSingleRuntimeTestManager(engine, session.NewJSONLBackend(canonical)), canonical
}

func TestSeahorseReconciliationCleanRevisionSkipsDeepComparison(t *testing.T) {
	mgr, canonical := newReconciliationTestManager(t)
	ctx := context.Background()
	key := "clean"
	if err := canonical.AddMessage(ctx, key, "user", "canonical"); err != nil {
		t.Fatal(err)
	}
	if reconcileErr := mgr.ensureReconciled(ctx, key, singleTestRuntime(mgr).sessions); reconcileErr != nil {
		t.Fatal(reconcileErr)
	}
	before := mgr.reconciliations.Load()
	if err := mgr.ensureReconciled(ctx, key, singleTestRuntime(mgr).sessions); err != nil {
		t.Fatal(err)
	}
	if got := mgr.reconciliations.Load(); got != before {
		t.Fatalf("unchanged revision reconciliations = %d, want %d", got, before)
	}
}

func TestSeahorseCompactCorrelatesStableReconciledRevision(t *testing.T) {
	key := "stable-revision"
	engine, err := seahorse.NewEngine(seahorse.Config{DBPath: t.TempDir() + "/seahorse.db"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	store := &advancingRevisionSessionStore{staticHistorySessionStore: &staticHistorySessionStore{
		SessionStore: session.NewMemoryStore(),
		history:      []providers.Message{{Role: "user", Content: "canonical"}},
		revision:     memory.HistoryRevision{Revision: 7, Count: 1},
	}}
	runtimeBus := runtimeevents.NewBus()
	t.Cleanup(func() { _ = runtimeBus.Close() })
	subscription, events, err := runtimeBus.Channel().
		OfKind(runtimeevents.KindAgentContextCompressStart, runtimeevents.KindAgentContextCompressEnd).
		SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{Name: "stable-revision", Buffer: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = subscription.Close() })
	manager := newSingleRuntimeTestManager(engine, store)
	manager.al = &AgentLoop{runtimeEvents: runtimeBus}
	if err := manager.Compact(
		t.Context(),
		&CompactRequest{SessionKey: key, Reason: ContextCompressReasonProactive},
	); err != nil {
		t.Fatal(err)
	}
	started := receiveRuntimeEvent(t, events)
	ended := receiveRuntimeEvent(t, events)
	startPayload := started.Payload.(ContextCompressLifecyclePayload)
	endPayload := ended.Payload.(ContextCompressLifecyclePayload)
	if startPayload.TranscriptRevision != 8 || startPayload.TranscriptCount != 2 ||
		endPayload.TranscriptRevision != 8 || endPayload.TranscriptCount != 2 {
		t.Fatalf("lifecycle revisions = start:%+v end:%+v", startPayload, endPayload)
	}
	store.mu.Lock()
	calls := store.calls
	store.mu.Unlock()
	if calls != 3 {
		t.Fatalf("history revision reads = %d, want initial plus stable snapshot pair", calls)
	}
}

func TestSeahorseReconciliationDoesNotTurnUnknownTimeIntoRecencyEvidence(t *testing.T) {
	tests := []struct {
		name      string
		createdAt *time.Time
	}{
		{name: "nil"},
		{name: "zero", createdAt: new(time.Time)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
			key := "unknown-time-" + test.name
			engine, err := seahorse.NewEngine(seahorse.Config{DBPath: t.TempDir() + "/seahorse.db"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = engine.Close() })

			revision := memory.HistoryRevision{Revision: 1, Count: 1}
			store := &staticHistorySessionStore{
				SessionStore: session.NewMemoryStore(),
				history: []providers.Message{{
					Role:      "user",
					Content:   "Here is what I ate",
					CreatedAt: test.createdAt,
				}},
				revision: revision,
			}
			manager := newSingleRuntimeTestManager(engine, store)

			_, err = engine.Ingest(ctx, key, []seahorse.Message{{
				Role:      "user",
				Content:   "Here is what I ate",
				CreatedAt: now,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.GetRetrieval().Store().SetReconciliationState(ctx, seahorse.ReconciliationState{
				SessionKey:       key,
				SourceRevision:   revision.Revision,
				SourceCount:      revision.Count,
				SchemaGeneration: seahorseReconciliationGeneration - 1,
			}); err != nil {
				t.Fatal(err)
			}

			assembled, err := manager.Assemble(ctx, &AssembleRequest{SessionKey: key, Budget: 1000})
			if err != nil {
				t.Fatal(err)
			}
			if len(assembled.History) != 1 || assembled.History[0].CreatedAt != nil {
				t.Fatalf("assembled history = %#v, want one message with unknown time", assembled.History)
			}
			relation := classifyPromptCurrentMessageRelation(
				"[image]",
				[]string{"data:image/png;base64,abc123"},
				"",
				true,
				assembled.History,
				now,
			)
			if relation.Kind != InboundRelationStandalone {
				t.Fatalf("relation = %#v, want standalone", relation)
			}
		})
	}
}

func TestSeahorseClearStopsBeforeDerivedStateWhenCanonicalClearFails(t *testing.T) {
	mgr, canonical := newReconciliationTestManager(t)
	ctx := t.Context()
	const key = "clear-failure"
	if err := canonical.AddMessage(ctx, key, "user", "retained"); err != nil {
		t.Fatal(err)
	}
	runtime := singleTestRuntime(mgr)
	if err := mgr.ensureReconciled(ctx, key, runtime.sessions); err != nil {
		t.Fatal(err)
	}
	injectedErr := errors.New("canonical clear failed")
	runtime.sessions = &clearFailingSessionStore{SessionStore: runtime.sessions, err: injectedErr}

	err := mgr.Clear(ctx, &AgentInstance{ID: runtime.agentID}, key)
	if !errors.Is(err, injectedErr) {
		t.Fatalf("Clear() error = %v, want %v", err, injectedErr)
	}
	conversation, err := runtime.engine.GetRetrieval().Store().GetConversationBySessionKey(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := runtime.engine.GetRetrieval().Store().GetMessages(ctx, conversation.ConversationID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Content != "retained" {
		t.Fatalf("derived messages after failed clear = %#v", messages)
	}
}

func TestSeahorseContextManagerPersistsTrustedConversationProvenance(t *testing.T) {
	mgr, _ := newReconciliationTestManager(t)
	ctx := context.Background()
	key := session.BuildOpaqueSessionKey("test|epoch=daily")
	metadataStore := singleTestRuntime(mgr).sessions.(session.MetadataAwareSessionStore)
	metadataStore.EnsureSessionMetadata(key, &session.SessionScope{
		Version:       session.ScopeVersion,
		AgentID:       "nutrition",
		RouteScopeKey: "telegram:account:chat:topic",
	})

	if err := mgr.Ingest(ctx, &IngestRequest{
		SessionKey: key,
		Message:    providers.Message{Role: "user", Content: "breakfast"},
	}); err != nil {
		t.Fatal(err)
	}
	conversation, err := singleTestRuntime(mgr).engine.GetRetrieval().Store().GetConversationBySessionKey(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if conversation == nil || conversation.RouteScopeKey != "telegram:account:chat:topic" ||
		conversation.AgentID != "nutrition" {
		t.Fatalf("conversation provenance = %#v", conversation)
	}
}

func TestSeahorseReconciliationCleanRestartUsesDurableWatermark(t *testing.T) {
	dir := t.TempDir()
	canonical, err := memory.NewJSONLStore(dir + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	backend := session.NewJSONLBackend(canonical)
	ctx := context.Background()
	key := "restart"
	if err := canonical.AddMessage(ctx, key, "user", "persisted"); err != nil {
		t.Fatal(err)
	}
	engine1, engineErr := seahorse.NewEngine(seahorse.Config{DBPath: dir + "/seahorse.db"}, nil)
	if engineErr != nil {
		t.Fatal(engineErr)
	}
	mgr1 := newSingleRuntimeTestManager(engine1, backend)
	if err := mgr1.ensureReconciled(ctx, key, backend); err != nil {
		t.Fatal(err)
	}
	if err := engine1.Close(); err != nil {
		t.Fatal(err)
	}
	engine2, reopenErr := seahorse.NewEngine(seahorse.Config{DBPath: dir + "/seahorse.db"}, nil)
	if reopenErr != nil {
		t.Fatal(reopenErr)
	}
	defer func() { _ = engine2.Close() }()
	mgr2 := newSingleRuntimeTestManager(engine2, backend)
	if err := mgr2.ensureReconciled(ctx, key, backend); err != nil {
		t.Fatal(err)
	}
	if got := mgr2.reconciliations.Load(); got != 0 {
		t.Fatalf("clean restart performed %d full reconciliations", got)
	}
}

func TestSeahorseReconciliationAppendAndReplace(t *testing.T) {
	mgr, canonical := newReconciliationTestManager(t)
	ctx := context.Background()
	key := "mutations"
	first := providers.Message{Role: "user", Content: "one"}
	if err := canonical.AddFullMessage(ctx, key, first); err != nil {
		t.Fatal(err)
	}
	if err := mgr.ensureReconciled(ctx, key, singleTestRuntime(mgr).sessions); err != nil {
		t.Fatal(err)
	}
	second := providers.Message{Role: "assistant", Content: "two"}
	if err := canonical.AddFullMessage(ctx, key, second); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Ingest(ctx, &IngestRequest{SessionKey: key, Message: second}); err != nil {
		t.Fatal(err)
	}
	if err := canonical.SetHistory(ctx, key, []providers.Message{{Role: "user", Content: "replacement"}}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.ensureReconciled(ctx, key, singleTestRuntime(mgr).sessions); err != nil {
		t.Fatal(err)
	}
	engine := singleTestRuntime(mgr).engine
	conv, _ := engine.GetRetrieval().Store().GetConversationBySessionKey(ctx, key)
	messages, _ := engine.GetRetrieval().Store().GetMessages(ctx, conv.ConversationID, 0, 0)
	if len(messages) != 1 || messages[0].Content != "replacement" {
		t.Fatalf("reconciled messages = %#v", messages)
	}
}

func TestSeahorseFastIngestPreservesCanonicalTimestamp(t *testing.T) {
	tests := []struct {
		name      string
		createdAt *time.Time
	}{
		{name: "nil"},
		{name: "zero", createdAt: new(time.Time)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, canonical := newReconciliationTestManager(t)
			ctx := t.Context()
			key := "fast-ingest-timestamp-" + test.name
			runtime := singleTestRuntime(manager)

			if err := canonical.AddMessage(ctx, key, "assistant", "existing history"); err != nil {
				t.Fatal(err)
			}
			if err := manager.ensureReconciled(ctx, key, runtime.sessions); err != nil {
				t.Fatal(err)
			}

			message := providers.Message{
				Role:      "user",
				Content:   "Here is what I ate",
				CreatedAt: test.createdAt,
			}
			if err := persistFullSessionMessage(ctx, runtime.sessions, key, &message); err != nil {
				t.Fatal(err)
			}
			if message.CreatedAt == nil || message.CreatedAt.IsZero() {
				t.Fatalf("canonical message timestamp = %v, want current time", message.CreatedAt)
			}
			before := manager.reconciliations.Load()
			if err := manager.Ingest(ctx, &IngestRequest{SessionKey: key, Message: message}); err != nil {
				t.Fatal(err)
			}
			if got := manager.reconciliations.Load(); got != before {
				t.Fatalf("fast ingest performed %d full reconciliations, want %d", got, before)
			}

			assembled, err := manager.Assemble(ctx, &AssembleRequest{SessionKey: key, Budget: 1000})
			if err != nil {
				t.Fatal(err)
			}
			if len(assembled.History) != 2 {
				t.Fatalf("assembled history = %#v, want two messages", assembled.History)
			}
			got := assembled.History[1].CreatedAt
			want := normalizeSeahorseMessageCreatedAt(message.CreatedAt)
			if got == nil || !got.Equal(want) {
				t.Fatalf("assembled timestamp = %v, want %v", got, want)
			}
		})
	}
}

func TestSeahorseReconciliationGenerationAndFailureRetry(t *testing.T) {
	mgr, canonical := newReconciliationTestManager(t)
	key := "retry"
	if err := canonical.AddMessage(context.Background(), key, "user", "source"); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mgr.ensureReconciled(canceled, key, singleTestRuntime(mgr).sessions); err == nil {
		t.Fatal("expected canceled reconciliation to fail")
	}
	store := singleTestRuntime(mgr).engine.GetRetrieval().Store()
	state, _ := store.GetReconciliationState(context.Background(), key)
	if state != nil {
		t.Fatal("failed reconciliation advanced watermark")
	}
	if err := mgr.ensureReconciled(context.Background(), key, singleTestRuntime(mgr).sessions); err != nil {
		t.Fatal(err)
	}
	state, _ = store.GetReconciliationState(context.Background(), key)
	state.SchemaGeneration--
	if err := store.SetReconciliationState(context.Background(), *state); err != nil {
		t.Fatal(err)
	}
	before := mgr.reconciliations.Load()
	if err := mgr.ensureReconciled(context.Background(), key, singleTestRuntime(mgr).sessions); err != nil {
		t.Fatal(err)
	}
	if mgr.reconciliations.Load() != before+1 {
		t.Fatal("schema generation change did not force reconciliation")
	}
	state, _ = store.GetReconciliationState(context.Background(), key)
	if state.SchemaGeneration != seahorseReconciliationGeneration {
		t.Fatalf("generation = %d", state.SchemaGeneration)
	}
	conversation, err := store.GetConversationBySessionKey(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	staleSummary, err := store.CreateSummary(context.Background(), seahorse.CreateSummaryInput{
		ConversationID: conversation.ConversationID,
		Kind:           seahorse.SummaryKindLeaf,
		Content:        "stale personal-policy summary",
		TokenCount:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendContextSummary(
		context.Background(),
		conversation.ConversationID,
		staleSummary.SummaryID,
	); err != nil {
		t.Fatal(err)
	}

	runtime := singleTestRuntime(mgr)
	runtime.reconciliationGeneration = seahorse.SummaryPolicyCodingV1.ReconciliationGeneration(
		seahorseReconciliationGeneration,
	)
	before = mgr.reconciliations.Load()
	if err := mgr.ensureReconciled(context.Background(), key, runtime.sessions); err != nil {
		t.Fatal(err)
	}
	if mgr.reconciliations.Load() != before+1 {
		t.Fatal("summary policy version change did not force reconciliation")
	}
	state, _ = store.GetReconciliationState(context.Background(), key)
	if state.SchemaGeneration != runtime.reconciliationGeneration {
		t.Fatalf("policy generation = %d, want %d", state.SchemaGeneration, runtime.reconciliationGeneration)
	}
	if _, err := store.GetSummary(context.Background(), staleSummary.SummaryID); err == nil {
		t.Fatal("policy generation rebuild retained a stale summary")
	}
	items, err := store.GetContextItems(context.Background(), conversation.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ItemType != "message" {
		t.Fatalf("policy generation rebuild context = %#v, want canonical raw message", items)
	}
}

func TestSeahorseIngestKeepsLiveMessageAfterCanonicalWriteFailure(t *testing.T) {
	mgr, _ := newReconciliationTestManager(t)
	ctx := context.Background()
	const key = "failed-canonical-write"
	live := providers.Message{Role: "user", Content: "live despite disk failure"}
	if err := mgr.Ingest(ctx, &IngestRequest{
		SessionKey:        key,
		Message:           live,
		CanonicalWriteErr: errors.New("disk full"),
	}); err != nil {
		t.Fatal(err)
	}

	store := singleTestRuntime(mgr).engine.GetRetrieval().Store()
	conv, err := store.GetConversationBySessionKey(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetMessages(ctx, conv.ConversationID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Content != live.Content {
		t.Fatalf("live Seahorse messages = %#v", stored)
	}
	state, err := store.GetReconciliationState(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if state != nil {
		t.Fatalf("failed canonical write advanced watermark: %+v", state)
	}

	if reconcileErr := mgr.ensureReconciled(ctx, key, singleTestRuntime(mgr).sessions); reconcileErr != nil {
		t.Fatal(reconcileErr)
	}
	stored, err = store.GetMessages(ctx, conv.ConversationID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 0 {
		t.Fatalf("reconciliation did not restore canonical authority: %#v", stored)
	}
}

func TestSeahorseWriteErrorAfterDurableAppendDoesNotDuplicate(t *testing.T) {
	mgr, canonical := newReconciliationTestManager(t)
	ctx := context.Background()
	const key = "durable-before-error"
	live := providers.Message{Role: "user", Content: "already canonical"}
	if err := canonical.AddFullMessage(ctx, key, live); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Ingest(ctx, &IngestRequest{
		SessionKey:        key,
		Message:           live,
		CanonicalWriteErr: errors.New("fsync result unknown"),
	}); err != nil {
		t.Fatal(err)
	}

	store := singleTestRuntime(mgr).engine.GetRetrieval().Store()
	conv, err := store.GetConversationBySessionKey(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetMessages(ctx, conv.ConversationID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Content != live.Content {
		t.Fatalf("durable append produced duplicate messages: %#v", stored)
	}
}

func TestSeahorseConcurrentLiveIngestDoesNotDuplicate(t *testing.T) {
	mgr, canonical := newReconciliationTestManager(t)
	ctx := context.Background()
	key := "concurrent"
	const count = 20
	messages := make([]providers.Message, count)
	for i := range messages {
		messages[i] = providers.Message{Role: "user", Content: fmt.Sprintf("message-%d", i)}
		if err := canonical.AddFullMessage(ctx, key, messages[i]); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := range messages {
		wg.Add(1)
		go func(msg providers.Message) {
			defer wg.Done()
			if err := mgr.Ingest(ctx, &IngestRequest{SessionKey: key, Message: msg}); err != nil {
				t.Errorf("Ingest: %v", err)
			}
		}(messages[i])
	}
	wg.Wait()
	engine := singleTestRuntime(mgr).engine
	conv, _ := engine.GetRetrieval().Store().GetConversationBySessionKey(ctx, key)
	stored, _ := engine.GetRetrieval().Store().GetMessages(ctx, conv.ConversationID, 0, 0)
	if len(stored) != count {
		t.Fatalf("stored %d messages, want %d", len(stored), count)
	}
}

func TestSeahorseReconciliationUsesRoutedSessionOwner(t *testing.T) {
	mgr, _ := newReconciliationTestManager(t)
	mainStore := session.NewMemoryStore()
	supportStore := session.NewMemoryStore()
	key := "agent:support:direct:42"
	supportStore.AddMessage(key, "user", "owned by support")
	singleTestRuntime(mgr).sessions = mainStore
	supportAgent := &AgentInstance{ID: "support", Sessions: supportStore}
	mgr.al = &AgentLoop{registry: &AgentRegistry{
		agents: map[string]*AgentInstance{
			"main":    {ID: "main", Sessions: mainStore},
			"support": supportAgent,
		},
	}}
	if err := mgr.ensureReconciled(context.Background(), key, supportAgent.Sessions); err != nil {
		t.Fatal(err)
	}
	engine := singleTestRuntime(mgr).engine
	conv, _ := engine.GetRetrieval().Store().GetConversationBySessionKey(context.Background(), key)
	stored, _ := engine.GetRetrieval().Store().GetMessages(context.Background(), conv.ConversationID, 0, 0)
	if len(stored) != 1 || stored[0].Content != "owned by support" {
		t.Fatalf("routed messages = %#v", stored)
	}
}

func BenchmarkSeahorseCleanRevisionCheck(b *testing.B) {
	dir := b.TempDir()
	canonical, _ := memory.NewJSONLStore(dir + "/sessions")
	backend := session.NewJSONLBackend(canonical)
	engine, _ := seahorse.NewEngine(seahorse.Config{DBPath: dir + "/seahorse.db"}, nil)
	defer func() { _ = engine.Close() }()
	mgr := newSingleRuntimeTestManager(engine, backend)
	ctx := context.Background()
	_ = canonical.AddMessage(ctx, "bench", "user", "hello")
	_ = mgr.ensureReconciled(ctx, "bench", backend)
	b.ResetTimer()
	for range b.N {
		if err := mgr.ensureReconciled(ctx, "bench", backend); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSeahorseForcedReconciliation100Messages(b *testing.B) {
	dir := b.TempDir()
	canonical, _ := memory.NewJSONLStore(dir + "/sessions")
	backend := session.NewJSONLBackend(canonical)
	engine, _ := seahorse.NewEngine(seahorse.Config{DBPath: dir + "/seahorse.db"}, nil)
	defer func() { _ = engine.Close() }()
	mgr := newSingleRuntimeTestManager(engine, backend)
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		_ = canonical.AddMessage(ctx, "bench-full", "user", fmt.Sprintf("message-%d", i))
	}
	_ = mgr.ensureReconciled(ctx, "bench-full", backend)
	store := engine.GetRetrieval().Store()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		state, _ := store.GetReconciliationState(ctx, "bench-full")
		state.SchemaGeneration--
		_ = store.SetReconciliationState(ctx, *state)
		b.StartTimer()
		if err := mgr.ensureReconciled(ctx, "bench-full", backend); err != nil {
			b.Fatal(err)
		}
	}
}
