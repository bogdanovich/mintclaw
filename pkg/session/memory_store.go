package session

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/providers/messageutil"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
)

// MemoryStore is a non-persistent SessionStore for tests, benchmarks, and
// explicitly ephemeral runtimes. Persistent MintClaw runtimes use JSONLBackend.
type MemoryStore struct {
	sessions map[string]*memorySession
	mu       sync.RWMutex
}

type memorySession struct {
	messages        []providers.Message
	summary         string
	historyRevision uint64
}

// NewMemoryStore creates an empty in-memory session store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: make(map[string]*memorySession)}
}

func (s *memorySession) advanceHistoryRevision() {
	s.historyRevision++
	if s.historyRevision == 0 {
		s.historyRevision = 1
	}
}

func (s *MemoryStore) sessionLocked(key string) *memorySession {
	stored := s.sessions[key]
	if stored == nil {
		stored = &memorySession{}
		s.sessions[key] = stored
	}
	return stored
}

func (s *MemoryStore) GetHistoryRevision(key string) (memory.HistoryRevision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stored := s.sessions[key]
	if stored == nil {
		return memory.HistoryRevision{}, nil
	}
	return memory.HistoryRevision{
		Revision: stored.historyRevision,
		Count:    len(stored.messages),
	}, nil
}

func ensureMessageCreatedAt(msg *providers.Message, fallback time.Time) {
	if msg.CreatedAt != nil && !msg.CreatedAt.IsZero() {
		return
	}
	createdAt := fallback
	msg.CreatedAt = &createdAt
}

func normalizeHistoryCreatedAt(history []providers.Message) {
	now := time.Now()
	for index := range history {
		ensureMessageCreatedAt(&history[index], now)
	}
}

func cloneSessionMessage(message providers.Message) providers.Message {
	cloned := message
	if message.CreatedAt != nil {
		createdAt := *message.CreatedAt
		cloned.CreatedAt = &createdAt
	}
	cloned.Deliverable = taskresult.CloneDeliverable(message.Deliverable)
	return cloned
}

func cloneSessionMessages(messages []providers.Message) []providers.Message {
	if messages == nil {
		return nil
	}
	cloned := make([]providers.Message, len(messages))
	for index, message := range messages {
		cloned[index] = cloneSessionMessage(message)
	}
	return cloned
}

func (s *MemoryStore) AddMessage(sessionKey, role, content string) {
	s.AddFullMessage(sessionKey, providers.Message{Role: role, Content: content})
}

func (s *MemoryStore) AddFullMessage(sessionKey string, msg providers.Message) {
	_ = s.AppendTurnMessage(context.Background(), sessionKey, msg)
}

func (s *MemoryStore) AppendTurnMessage(ctx context.Context, sessionKey string, msg providers.Message) error {
	if err := contextCause(ctx); err != nil {
		return err
	}
	if messageutil.IsTransientAssistantThoughtMessage(msg) {
		return nil
	}
	msg = cloneSessionMessage(msg)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextCause(ctx); err != nil {
		return err
	}
	now := time.Now()
	ensureMessageCreatedAt(&msg, now)
	stored := s.sessionLocked(sessionKey)
	stored.messages = append(stored.messages, msg)
	stored.advanceHistoryRevision()
	return nil
}

func (s *MemoryStore) RestoreTurnSnapshot(
	ctx context.Context,
	sessionKey string,
	history []providers.Message,
	summary string,
) error {
	return s.replaceTurnSnapshot(ctx, sessionKey, history, summary, true)
}

func (s *MemoryStore) ReplaceTurnHistory(
	ctx context.Context,
	sessionKey string,
	history []providers.Message,
) error {
	return s.replaceTurnSnapshot(ctx, sessionKey, history, "", false)
}

func (s *MemoryStore) MutateTurnHistory(
	ctx context.Context,
	sessionKey string,
	mutate func([]providers.Message) ([]providers.Message, bool, error),
) (bool, error) {
	if err := contextCause(ctx); err != nil {
		return false, err
	}
	if mutate == nil {
		return false, fmt.Errorf("session: history mutation callback is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextCause(ctx); err != nil {
		return false, err
	}

	stored := s.sessionLocked(sessionKey)
	history, changed, err := mutate(cloneSessionMessages(stored.messages))
	if err != nil || !changed {
		return false, err
	}
	stored.messages = cloneSessionMessages(messageutil.FilterInvalidHistoryMessages(history))
	normalizeHistoryCreatedAt(stored.messages)
	stored.advanceHistoryRevision()
	return true, nil
}

func (s *MemoryStore) ClearSession(ctx context.Context, sessionKey string) error {
	return s.RestoreTurnSnapshot(ctx, sessionKey, nil, "")
}

func (s *MemoryStore) replaceTurnSnapshot(
	ctx context.Context,
	sessionKey string,
	history []providers.Message,
	summary string,
	replaceSummary bool,
) error {
	if err := contextCause(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextCause(ctx); err != nil {
		return err
	}

	stored := s.sessionLocked(sessionKey)
	stored.messages = cloneSessionMessages(messageutil.FilterInvalidHistoryMessages(history))
	normalizeHistoryCreatedAt(stored.messages)
	if replaceSummary {
		stored.summary = summary
	}
	stored.advanceHistoryRevision()
	return nil
}

func (s *MemoryStore) GetHistory(key string) []providers.Message {
	history, _ := s.ReadTurnHistory(context.Background(), key)
	return history
}

func (s *MemoryStore) GetHistoryWithError(key string) ([]providers.Message, error) {
	return s.ReadTurnHistory(context.Background(), key)
}

func (s *MemoryStore) ReadTurnHistory(ctx context.Context, sessionKey string) ([]providers.Message, error) {
	if err := contextCause(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := contextCause(ctx); err != nil {
		return nil, err
	}
	stored := s.sessions[sessionKey]
	if stored == nil {
		return []providers.Message{}, nil
	}
	return cloneSessionMessages(stored.messages), nil
}

func (s *MemoryStore) ReadTurnHistoryPage(
	ctx context.Context,
	sessionKey string,
	request memory.HistoryPageRequest,
) (memory.HistoryPage, error) {
	if err := contextCause(ctx); err != nil {
		return memory.HistoryPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := contextCause(ctx); err != nil {
		return memory.HistoryPage{}, err
	}
	stored := s.sessions[sessionKey]
	if stored == nil {
		return sliceHistoryPage(nil, memory.HistoryRevision{}, request)
	}
	return sliceHistoryPage(stored.messages, memory.HistoryRevision{
		Revision: stored.historyRevision,
		Count:    len(stored.messages),
	}, request)
}

func (s *MemoryStore) GetSummary(key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stored := s.sessions[key]
	if stored == nil {
		return ""
	}
	return stored.summary
}

func (s *MemoryStore) SetSummary(key, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := s.sessionLocked(key)
	stored.summary = summary
}

func (s *MemoryStore) TruncateHistory(key string, keepLast int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := s.sessions[key]
	if stored == nil || len(stored.messages) == 0 || keepLast >= len(stored.messages) {
		return
	}
	if keepLast <= 0 {
		stored.messages = []providers.Message{}
	} else {
		stored.messages = cloneSessionMessages(stored.messages[len(stored.messages)-keepLast:])
	}
	stored.advanceHistoryRevision()
}

func (s *MemoryStore) ListSessions() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.sessions))
	for key := range s.sessions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *MemoryStore) Save(string) error {
	return nil
}

func (s *MemoryStore) Close() error {
	return nil
}

func (s *MemoryStore) SetHistory(key string, history []providers.Message) {
	_ = s.ReplaceTurnHistory(context.Background(), key, history)
}
