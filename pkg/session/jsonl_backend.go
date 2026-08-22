package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

// JSONLBackend adapts a memory.Store into the SessionStore interface.
// Write errors are logged rather than returned, matching the fire-and-forget
// contract of SessionManager that the agent loop relies on.
type JSONLBackend struct {
	store memory.Store
}

type metaAwareStore interface {
	GetSessionMeta(ctx context.Context, sessionKey string) (memory.SessionMeta, error)
	UpsertSessionMeta(ctx context.Context, sessionKey string, scope json.RawMessage) error
}

const maxTurnHistoryPageMessages = 256

// MetadataAwareSessionStore exposes structured session metadata operations.
type MetadataAwareSessionStore interface {
	EnsureSessionMetadata(sessionKey string, scope *SessionScope)
	GetSessionScope(sessionKey string) *SessionScope
}

// NewJSONLBackend wraps a memory.Store for use as a SessionStore.
func NewJSONLBackend(store memory.Store) *JSONLBackend {
	return &JSONLBackend{store: store}
}

// EnsureSessionMetadata persists structured scope metadata for a session.
func (b *JSONLBackend) EnsureSessionMetadata(sessionKey string, scope *SessionScope) {
	metaStore, ok := b.store.(metaAwareStore)
	if !ok {
		return
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return
	}

	var rawScope json.RawMessage
	if scope != nil {
		data, err := json.Marshal(scope)
		if err != nil {
			log.Printf("session: encode session scope: %v", err)
			return
		}
		rawScope = data
	}
	ctx := context.Background()
	if err := metaStore.UpsertSessionMeta(ctx, sessionKey, rawScope); err != nil {
		log.Printf("session: upsert session metadata: %v", err)
	}
}

// GetSessionScope reads structured scope metadata for a session key.
func (b *JSONLBackend) GetSessionScope(sessionKey string) *SessionScope {
	metaStore, ok := b.store.(metaAwareStore)
	if !ok {
		return nil
	}
	meta, err := metaStore.GetSessionMeta(context.Background(), sessionKey)
	if err != nil {
		log.Printf("session: get session metadata: %v", err)
		return nil
	}
	if len(meta.Scope) == 0 {
		return nil
	}
	var scope SessionScope
	if err := json.Unmarshal(meta.Scope, &scope); err != nil {
		log.Printf("session: decode session scope: %v", err)
		return nil
	}
	return CloneScope(&scope)
}

func (b *JSONLBackend) AddMessage(sessionKey, role, content string) {
	if err := b.AddMessageWithError(sessionKey, role, content); err != nil {
		log.Printf("session: add message: %v", err)
	}
}

func (b *JSONLBackend) AddMessageWithError(sessionKey, role, content string) error {
	return b.AppendTurnMessage(context.Background(), sessionKey, providers.Message{Role: role, Content: content})
}

func (b *JSONLBackend) AddFullMessage(sessionKey string, msg providers.Message) {
	if err := b.AddFullMessageWithError(sessionKey, msg); err != nil {
		log.Printf("session: add full message: %v", err)
	}
}

func (b *JSONLBackend) AddFullMessageWithError(sessionKey string, msg providers.Message) error {
	return b.AppendTurnMessage(context.Background(), sessionKey, msg)
}

func (b *JSONLBackend) AppendTurnMessage(
	ctx context.Context,
	sessionKey string,
	msg providers.Message,
) error {
	if err := contextCause(ctx); err != nil {
		return err
	}
	if err := contextCause(ctx); err != nil {
		return err
	}
	return b.store.AddFullMessage(ctx, sessionKey, msg)
}

func (b *JSONLBackend) RestoreTurnSnapshot(
	ctx context.Context,
	sessionKey string,
	history []providers.Message,
	summary string,
) error {
	if err := contextCause(ctx); err != nil {
		return err
	}
	if err := b.store.SetHistory(ctx, sessionKey, history); err != nil {
		return fmt.Errorf("restore history: %w", err)
	}
	if err := b.store.SetSummary(ctx, sessionKey, summary); err != nil {
		return fmt.Errorf("restore summary: %w", err)
	}
	return nil
}

func (b *JSONLBackend) ReplaceTurnHistory(
	ctx context.Context,
	sessionKey string,
	history []providers.Message,
) error {
	if err := contextCause(ctx); err != nil {
		return err
	}
	if err := contextCause(ctx); err != nil {
		return err
	}
	return b.store.SetHistory(ctx, sessionKey, history)
}

func (b *JSONLBackend) MutateTurnHistory(
	ctx context.Context,
	sessionKey string,
	mutate func([]providers.Message) ([]providers.Message, bool, error),
) (bool, error) {
	if err := contextCause(ctx); err != nil {
		return false, err
	}
	store, ok := b.store.(memory.HistoryMutationStore)
	if !ok {
		return false, fmt.Errorf("session: atomic history mutation unsupported")
	}
	return store.MutateHistory(ctx, sessionKey, mutate)
}

func (b *JSONLBackend) ReadTurnHistory(ctx context.Context, sessionKey string) ([]providers.Message, error) {
	if err := contextCause(ctx); err != nil {
		return nil, err
	}
	if err := contextCause(ctx); err != nil {
		return nil, err
	}
	return b.store.GetHistory(ctx, sessionKey)
}

// ReadTurnHistoryPage reads a bounded canonical-history window when the
// backing store supports it, with a compatibility fallback for other stores.
func (b *JSONLBackend) ReadTurnHistoryPage(
	ctx context.Context,
	sessionKey string,
	request memory.HistoryPageRequest,
) (memory.HistoryPage, error) {
	if err := contextCause(ctx); err != nil {
		return memory.HistoryPage{}, err
	}
	if paged, ok := b.store.(memory.HistoryPageStore); ok {
		return paged.GetHistoryPage(ctx, sessionKey, request)
	}
	history, err := b.store.GetHistory(ctx, sessionKey)
	if err != nil {
		return memory.HistoryPage{}, err
	}
	return sliceHistoryPage(history, memory.HistoryRevision{Count: len(history)}, request)
}

func sliceHistoryPage(
	history []providers.Message,
	revision memory.HistoryRevision,
	request memory.HistoryPageRequest,
) (memory.HistoryPage, error) {
	if request.Limit <= 0 || request.Limit > maxTurnHistoryPageMessages {
		return memory.HistoryPage{}, fmt.Errorf(
			"session: history page limit must be within 1..%d",
			maxTurnHistoryPageMessages,
		)
	}
	total := len(history)
	cursorTotal := total
	if request.Cursor != nil {
		cursorTotal = request.Cursor.Total
		if cursorTotal < 0 || cursorTotal > total {
			return memory.HistoryPage{}, fmt.Errorf(
				"%w: canonical prefix length changed",
				memory.ErrHistoryCursorStale,
			)
		}
	}
	cursor, err := memory.HistoryCursorForMessages(history, cursorTotal)
	if err != nil {
		return memory.HistoryPage{}, err
	}
	if request.Cursor != nil && *request.Cursor != cursor {
		return memory.HistoryPage{}, fmt.Errorf("%w: canonical prefix changed", memory.ErrHistoryCursorStale)
	}
	end := request.Before
	if end < 0 || end > cursorTotal {
		end = cursorTotal
	}
	start := max(0, end-request.Limit)
	return memory.HistoryPage{
		Messages: cloneSessionMessages(history[start:end]),
		Revision: revision,
		Cursor:   cursor,
		Start:    start,
		End:      end,
		Total:    cursorTotal,
		HasOlder: start > 0,
		HasNewer: end < cursorTotal,
	}, nil
}

func (b *JSONLBackend) ClearSession(ctx context.Context, sessionKey string) error {
	return b.RestoreTurnSnapshot(ctx, sessionKey, nil, "")
}

func (b *JSONLBackend) GetHistory(key string) []providers.Message {
	msgs, err := b.GetHistoryWithError(key)
	if err != nil {
		log.Printf("session: get history: %v", err)
		return []providers.Message{}
	}
	return msgs
}

func (b *JSONLBackend) GetHistoryWithError(key string) ([]providers.Message, error) {
	return b.ReadTurnHistory(context.Background(), key)
}

func (b *JSONLBackend) GetSummary(key string) string {
	summary, err := b.store.GetSummary(context.Background(), key)
	if err != nil {
		log.Printf("session: get summary: %v", err)
		return ""
	}
	return summary
}

func (b *JSONLBackend) SetSummary(key, summary string) {
	if err := b.store.SetSummary(context.Background(), key, summary); err != nil {
		log.Printf("session: set summary: %v", err)
	}
}

func (b *JSONLBackend) SetHistory(key string, history []providers.Message) {
	if err := b.store.SetHistory(context.Background(), key, history); err != nil {
		log.Printf("session: set history: %v", err)
	}
}

func (b *JSONLBackend) TruncateHistory(key string, keepLast int) {
	if err := b.store.TruncateHistory(context.Background(), key, keepLast); err != nil {
		log.Printf("session: truncate history: %v", err)
	}
}

// Save persists session state. Since the JSONL store fsyncs every write
// immediately, the data is already durable. Save runs compaction to reclaim
// space from logically truncated messages (no-op when there are none).
func (b *JSONLBackend) Save(key string) error {
	return b.store.Compact(context.Background(), key)
}

// Close releases resources held by the underlying store.
func (b *JSONLBackend) Close() error {
	return b.store.Close()
}

// ListSessions returns all known session keys.
func (b *JSONLBackend) ListSessions() []string {
	return b.store.ListSessions()
}

// GetHistoryRevision returns the canonical history identity when supported by
// the underlying store.
func (b *JSONLBackend) GetHistoryRevision(sessionKey string) (memory.HistoryRevision, error) {
	store, ok := b.store.(memory.HistoryRevisionStore)
	if !ok {
		return memory.HistoryRevision{}, fmt.Errorf("session: history revision unsupported")
	}
	return store.GetHistoryRevision(context.Background(), sessionKey)
}
