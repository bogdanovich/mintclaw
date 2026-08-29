package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

type snapshotFailingStore struct {
	memory.Store
	historyErr error
	summaryErr error
}

type metadataCapabilityStub struct{}

func (*metadataCapabilityStub) EnsureSessionMetadata(string, *session.SessionScope) {}

func (*metadataCapabilityStub) GetSessionScope(string) *session.SessionScope { return nil }

func (*metadataCapabilityStub) ClearSessionClientIDs(string) error { return nil }

var _ session.MetadataAwareSessionStore = (*metadataCapabilityStub)(nil)

func (s *snapshotFailingStore) SetHistory(
	ctx context.Context,
	sessionKey string,
	history []providers.Message,
) error {
	if s.historyErr != nil {
		return s.historyErr
	}
	return s.Store.SetHistory(ctx, sessionKey, history)
}

func (s *snapshotFailingStore) SetSummary(ctx context.Context, sessionKey, summary string) error {
	if s.summaryErr != nil {
		return s.summaryErr
	}
	return s.Store.SetSummary(ctx, sessionKey, summary)
}

func TestJSONLBackendTurnJournalHonorsCancellation(t *testing.T) {
	backend := newBackend(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := backend.AppendTurnMessage(ctx, "turn", providers.Message{Role: "user", Content: "canceled"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("AppendTurnMessage() error = %v, want %v", err, context.Canceled)
	}
	if history := backend.GetHistory("turn"); len(history) != 0 {
		t.Fatalf("canceled append mutated history: %+v", history)
	}
}

func TestJSONLBackendHistoryReplacementHonorsCancellation(t *testing.T) {
	backend := newBackend(t)
	if err := backend.AppendTurnMessage(
		t.Context(),
		"turn",
		providers.Message{Role: "user", Content: "current"},
	); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := backend.ReplaceTurnHistory(
		ctx,
		"turn",
		[]providers.Message{{Role: "user", Content: "replacement"}},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReplaceTurnHistory() error = %v, want %v", err, context.Canceled)
	}
	history := backend.GetHistory("turn")
	if len(history) != 1 || history[0].Content != "current" {
		t.Fatalf("canceled replacement mutated history: %+v", history)
	}
}

func TestJSONLBackendClearSessionClearsHistoryAndSummary(t *testing.T) {
	backend := newBackend(t)
	if err := backend.AppendTurnMessage(
		t.Context(),
		"turn",
		providers.Message{Role: "user", Content: "current"},
	); err != nil {
		t.Fatal(err)
	}
	backend.SetSummary("turn", "current summary")
	if err := backend.ClearSession(t.Context(), "turn"); err != nil {
		t.Fatalf("ClearSession() error = %v", err)
	}
	if history := backend.GetHistory("turn"); len(history) != 0 {
		t.Fatalf("history = %+v", history)
	}
	if summary := backend.GetSummary("turn"); summary != "" {
		t.Fatalf("summary = %q", summary)
	}
}

func TestJSONLBackendRestoreTurnSnapshotPropagatesReplacementFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		historyErr error
		summaryErr error
	}{
		{name: "history", historyErr: errors.New("replace history")},
		{name: "summary", summaryErr: errors.New("replace summary")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := memory.NewJSONLStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			backend := session.NewJSONLBackend(&snapshotFailingStore{
				Store:      store,
				historyErr: tc.historyErr,
				summaryErr: tc.summaryErr,
			})
			if appendErr := store.AddFullMessage(
				t.Context(),
				"turn",
				providers.Message{Role: "user", Content: "admitted root"},
			); appendErr != nil {
				t.Fatal(appendErr)
			}

			err = backend.RestoreTurnSnapshot(
				t.Context(),
				"turn",
				[]providers.Message{{Role: "user", Content: "before"}},
				"before summary",
			)
			wantErr := tc.historyErr
			if wantErr == nil {
				wantErr = tc.summaryErr
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("RestoreTurnSnapshot() error = %v, want %v", err, wantErr)
			}
		})
	}
}

// Compile-time interface satisfaction checks.
var (
	_ session.SessionStore = (*session.MemoryStore)(nil)
	_ session.SessionStore = (*session.JSONLBackend)(nil)
)

func newBackend(t *testing.T) *session.JSONLBackend {
	t.Helper()
	store, err := memory.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return session.NewJSONLBackend(store)
}

func TestJSONLBackend_AddAndGetHistory(t *testing.T) {
	b := newBackend(t)

	b.AddMessage("s1", "user", "hello")
	b.AddMessage("s1", "assistant", "hi")

	history := b.GetHistory("s1")
	if len(history) != 2 {
		t.Fatalf("got %d messages, want 2", len(history))
	}
	if history[0].Role != "user" || history[0].Content != "hello" {
		t.Errorf("msg[0] = %+v", history[0])
	}
	if history[1].Role != "assistant" || history[1].Content != "hi" {
		t.Errorf("msg[1] = %+v", history[1])
	}
}

func TestJSONLBackend_AddFullMessage(t *testing.T) {
	b := newBackend(t)

	msg := providers.Message{
		Role:    "assistant",
		Content: "done",
		ToolCalls: []providers.ToolCall{
			{ID: "tc1", Name: "read_file", Arguments: map[string]any{"path": "x"}},
		},
	}
	b.AddFullMessage("s1", msg)

	history := b.GetHistory("s1")
	if len(history) != 1 {
		t.Fatalf("got %d, want 1", len(history))
	}
	if len(history[0].ToolCalls) != 1 || history[0].ToolCalls[0].ID != "tc1" {
		t.Errorf("tool calls = %+v", history[0].ToolCalls)
	}
}

func TestJSONLBackend_AddFullMessage_PreservesModelName(t *testing.T) {
	b := newBackend(t)

	msg := providers.Message{
		Role:      "assistant",
		Content:   "done",
		ModelName: "gpt-5.4-mini",
	}
	b.AddFullMessage("s1", msg)

	history := b.GetHistory("s1")
	if len(history) != 1 {
		t.Fatalf("got %d, want 1", len(history))
	}
	if history[0].ModelName != "gpt-5.4-mini" {
		t.Fatalf("ModelName = %q, want %q", history[0].ModelName, "gpt-5.4-mini")
	}
}

func TestJSONLBackend_AddFullMessage_PreservesToolResultStatus(t *testing.T) {
	b := newBackend(t)
	b.AddFullMessage("s1", providers.Message{
		Role:             "tool",
		Content:          "failed",
		ToolCallID:       "call-1",
		ToolResultStatus: providers.ToolResultStatusError,
	})

	history := b.GetHistory("s1")
	if len(history) != 1 || history[0].ToolResultStatus != providers.ToolResultStatusError {
		t.Fatalf("tool result status = %#v", history)
	}
}

func TestJSONLBackend_Summary(t *testing.T) {
	b := newBackend(t)

	if got := b.GetSummary("s1"); got != "" {
		t.Errorf("got %q, want empty", got)
	}

	b.SetSummary("s1", "test summary")
	if got := b.GetSummary("s1"); got != "test summary" {
		t.Errorf("got %q, want %q", got, "test summary")
	}
}

func TestJSONLBackend_TruncateAndSave(t *testing.T) {
	b := newBackend(t)

	for i := 0; i < 10; i++ {
		b.AddMessage("s1", "user", fmt.Sprintf("msg %d", i))
	}
	b.TruncateHistory("s1", 3)

	history := b.GetHistory("s1")
	if len(history) != 3 {
		t.Fatalf("got %d, want 3", len(history))
	}
	if history[0].Content != "msg 7" {
		t.Errorf("got %q, want %q", history[0].Content, "msg 7")
	}

	// Save triggers compaction.
	if err := b.Save("s1"); err != nil {
		t.Fatal(err)
	}

	// Messages still accessible after compaction.
	history = b.GetHistory("s1")
	if len(history) != 3 {
		t.Fatalf("after save: got %d, want 3", len(history))
	}
}

func TestJSONLBackend_SetHistory(t *testing.T) {
	b := newBackend(t)
	b.AddMessage("s1", "user", "old")

	b.SetHistory("s1", []providers.Message{
		{Role: "user", Content: "new1"},
		{Role: "assistant", Content: "new2"},
	})

	history := b.GetHistory("s1")
	if len(history) != 2 {
		t.Fatalf("got %d, want 2", len(history))
	}
	if history[0].Content != "new1" {
		t.Errorf("got %q, want %q", history[0].Content, "new1")
	}
}

func TestJSONLBackend_EmptySession(t *testing.T) {
	b := newBackend(t)

	history := b.GetHistory("nonexistent")
	if history == nil {
		t.Fatal("got nil, want empty slice")
	}
	if len(history) != 0 {
		t.Errorf("got %d, want 0", len(history))
	}
}

func TestJSONLBackend_SessionIsolation(t *testing.T) {
	b := newBackend(t)
	b.AddMessage("s1", "user", "session1")
	b.AddMessage("s2", "user", "session2")

	h1 := b.GetHistory("s1")
	h2 := b.GetHistory("s2")

	if len(h1) != 1 || h1[0].Content != "session1" {
		t.Errorf("s1: %+v", h1)
	}
	if len(h2) != 1 || h2[0].Content != "session2" {
		t.Errorf("s2: %+v", h2)
	}
}

func TestJSONLBackend_SummarizeFlow(t *testing.T) {
	// Simulates the real summarization flow in the agent loop:
	// SetSummary → TruncateHistory → Save
	b := newBackend(t)

	for i := 0; i < 20; i++ {
		b.AddMessage("s1", "user", fmt.Sprintf("msg %d", i))
	}

	b.SetSummary("s1", "conversation about testing")
	b.TruncateHistory("s1", 4)
	if err := b.Save("s1"); err != nil {
		t.Fatal(err)
	}

	if got := b.GetSummary("s1"); got != "conversation about testing" {
		t.Errorf("summary = %q", got)
	}
	history := b.GetHistory("s1")
	if len(history) != 4 {
		t.Fatalf("got %d messages, want 4", len(history))
	}
	if history[0].Content != "msg 16" {
		t.Errorf("first message = %q, want %q", history[0].Content, "msg 16")
	}
}

func TestJSONLBackendPersistsScopeForExactSessionKey(t *testing.T) {
	b := newBackend(t)
	sessionKey := session.BuildOpaqueSessionKey("current-scope")

	scope := &session.SessionScope{
		Version:    session.ScopeVersion,
		AgentID:    "main",
		Channel:    "telegram",
		Account:    "default",
		Dimensions: []string{"chat"},
		Values: map[string]string{
			"chat": "group:c1",
		},
	}
	b.EnsureSessionMetadata(sessionKey, scope)
	b.AddMessage(sessionKey, "user", "hello")
	history := b.GetHistory(sessionKey)
	if len(history) != 1 {
		t.Fatalf("len(history) = %d, want 1", len(history))
	}
	if history[0].Content != "hello" {
		t.Fatalf("history[0].Content = %q, want %q", history[0].Content, "hello")
	}

	resolvedScope := b.GetSessionScope(sessionKey)
	if resolvedScope == nil {
		t.Fatal("GetSessionScope() returned nil")
	}
	if resolvedScope.AgentID != scope.AgentID || resolvedScope.Values["chat"] != scope.Values["chat"] {
		t.Fatalf("GetSessionScope() = %+v, want %+v", resolvedScope, scope)
	}
}

func TestJSONLBackendRejectsRemovedScopeVersion(t *testing.T) {
	store, err := memory.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	key := session.BuildOpaqueSessionKey("removed-scope-version")
	rawScope, err := json.Marshal(session.SessionScope{
		Version: session.ScopeVersion - 1,
		AgentID: "main",
		Channel: "mintclaw",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSessionMeta(t.Context(), key, rawScope, "removed-client"); err != nil {
		t.Fatal(err)
	}

	backend := session.NewJSONLBackend(store)
	if scope := backend.GetSessionScope(key); scope != nil {
		t.Fatalf("GetSessionScope() = %#v, want removed version rejected", scope)
	}
}

func TestJSONLBackendListsOnlyOwnedCurrentScopedSessions(t *testing.T) {
	store, err := memory.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	backend := session.NewJSONLBackend(store)

	currentKey := session.BuildOpaqueSessionKey("current-enumerated-session")
	backend.EnsureSessionMetadata(currentKey, &session.SessionScope{
		Version: session.ScopeVersion,
		AgentID: "Main",
		Channel: "mintclaw",
	})
	otherKey := session.BuildOpaqueSessionKey("other-current-enumerated-session")
	backend.EnsureSessionMetadata(otherKey, &session.SessionScope{
		Version: session.ScopeVersion,
		AgentID: "support",
		Channel: "mintclaw",
	})

	removedScopeKey := session.BuildOpaqueSessionKey("removed-enumerated-session")
	removedScope, err := json.Marshal(session.SessionScope{
		Version: session.ScopeVersion - 1,
		AgentID: "main",
		Channel: "mintclaw",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.UpsertSessionMeta(t.Context(), removedScopeKey, removedScope, ""); err != nil {
		t.Fatal(err)
	}

	currentScope, err := json.Marshal(session.SessionScope{
		Version: session.ScopeVersion,
		AgentID: "main",
		Channel: "mintclaw",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.UpsertSessionMeta(t.Context(), "task:removed-key", currentScope, ""); err != nil {
		t.Fatal(err)
	}
	missingScopeKey := session.BuildOpaqueSessionKey("missing-enumerated-scope")
	if err = store.AddFullMessage(
		t.Context(),
		missingScopeKey,
		providers.Message{Role: "user", Content: "missing scope"},
	); err != nil {
		t.Fatal(err)
	}

	keys := backend.ListCurrentAgentSessions("main")
	if len(keys) != 1 || keys[0] != currentKey {
		t.Fatalf("ListCurrentAgentSessions() = %v, want only %q", keys, currentKey)
	}
	if scope := backend.GetSessionScope(currentKey); scope == nil || scope.AgentID != "main" {
		t.Fatalf("GetSessionScope() = %#v, want canonical owner main", scope)
	}
	persisted, err := store.GetSessionMeta(t.Context(), currentKey)
	if err != nil {
		t.Fatal(err)
	}
	var persistedScope session.SessionScope
	if err = json.Unmarshal(persisted.Scope, &persistedScope); err != nil {
		t.Fatal(err)
	}
	if persistedScope.AgentID != "Main" {
		t.Fatalf("persisted owner = %q, want original Main unchanged", persistedScope.AgentID)
	}
}

func TestJSONLBackendRejectsUnknownCurrentScopeFields(t *testing.T) {
	store, err := memory.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	key := session.BuildOpaqueSessionKey("unknown-scope-field")
	rawScope := json.RawMessage(`{"version":2,"agent_id":"main","channel":"mintclaw","removed":true}`)
	if err = store.UpsertSessionMeta(t.Context(), key, rawScope, ""); err != nil {
		t.Fatal(err)
	}

	backend := session.NewJSONLBackend(store)
	if scope := backend.GetSessionScope(key); scope != nil {
		t.Fatalf("GetSessionScope() = %#v, want unknown field rejected", scope)
	}
	if keys := backend.ListCurrentAgentSessions("main"); len(keys) != 0 {
		t.Fatalf("ListCurrentAgentSessions() = %v, want unknown field omitted", keys)
	}
}

func TestJSONLBackendClearsAccumulatedClientSessionIDs(t *testing.T) {
	store, err := memory.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	key := session.BuildOpaqueSessionKey("durable-child")
	scope := &session.SessionScope{
		Version: session.ScopeVersion, AgentID: "child", ClientSessionID: "browser-parent",
	}
	backend := session.NewJSONLBackend(store)
	backend.EnsureSessionMetadata(key, scope)

	if err := backend.ClearSessionClientIDs(key); err != nil {
		t.Fatal(err)
	}
	meta, err := store.GetSessionMeta(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.ClientSessionIDs) != 0 {
		t.Fatalf("ClientSessionIDs = %v, want none", meta.ClientSessionIDs)
	}
	if resolved := backend.GetSessionScope(key); resolved == nil || resolved.AgentID != scope.AgentID {
		t.Fatalf("GetSessionScope() = %#v, want child scope preserved", resolved)
	}
}

func TestJSONLBackend_MutateTurnHistoryDoesNotLoseConcurrentAppend(t *testing.T) {
	b := newBackend(t)
	const sessionKey = "atomic-mutation"
	if err := b.AppendTurnMessage(
		context.Background(), sessionKey, providers.Message{Role: "assistant", Content: "intent"},
	); err != nil {
		t.Fatal(err)
	}
	mutationStarted := make(chan struct{})
	releaseMutation := make(chan struct{})
	appendStarted := make(chan struct{})
	appendDone := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var mutationErr, appendErr error
	go func() {
		defer wg.Done()
		_, mutationErr = b.MutateTurnHistory(
			context.Background(),
			sessionKey,
			func(history []providers.Message) ([]providers.Message, bool, error) {
				close(mutationStarted)
				<-releaseMutation
				history[0].Content = "intent with marker"
				return history, true, nil
			},
		)
	}()
	select {
	case <-mutationStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for atomic mutation")
	}
	go func() {
		defer wg.Done()
		defer close(appendDone)
		close(appendStarted)
		appendErr = b.AppendTurnMessage(
			context.Background(), sessionKey, providers.Message{Role: "user", Content: "concurrent append"},
		)
	}()
	<-appendStarted
	select {
	case <-appendDone:
		t.Fatal("concurrent append was not excluded by atomic history mutation")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseMutation)
	wg.Wait()
	if mutationErr != nil || appendErr != nil {
		t.Fatalf("mutation/append errors = %v / %v", mutationErr, appendErr)
	}
	history := b.GetHistory(sessionKey)
	if len(history) != 2 || history[0].Content != "intent with marker" || history[1].Content != "concurrent append" {
		t.Fatalf("canonical history lost a concurrent operation: %#v", history)
	}
}
