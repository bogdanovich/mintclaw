package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/routing"
)

// JSONLBackend adapts a memory.Store into the SessionStore interface.
// Passive administrative methods log write errors; turn-critical methods
// return them to the caller.
type JSONLBackend struct {
	store memory.Store
}

const maxTurnHistoryPageMessages = 256

var sessionScopeJSONFields = map[string]struct{}{
	"version":           {},
	"agent_id":          {},
	"channel":           {},
	"account":           {},
	"dimensions":        {},
	"values":            {},
	"route_scope_key":   {},
	"client_session_id": {},
	"epoch":             {},
}

var sessionEpochJSONFields = map[string]struct{}{
	"strategy": {},
	"id":       {},
	"start":    {},
}

// MetadataAwareSessionStore exposes structured session metadata operations.
type MetadataAwareSessionStore interface {
	EnsureSessionMetadata(sessionKey string, scope *SessionScope)
	GetSessionScope(sessionKey string) *SessionScope
	ClearSessionClientIDs(sessionKey string) error
}

// CurrentAgentSessionEnumerator lists current persisted sessions owned by one
// agent. Keeping this capability separate avoids changing unrelated metadata
// behavior for alternate SessionStore implementations.
type CurrentAgentSessionEnumerator interface {
	ListCurrentAgentSessions(agentID string) []string
}

// NewJSONLBackend wraps a memory.Store for use as a SessionStore.
func NewJSONLBackend(store memory.Store) *JSONLBackend {
	return &JSONLBackend{store: store}
}

// EnsureSessionMetadata persists structured scope metadata for a session.
func (b *JSONLBackend) EnsureSessionMetadata(sessionKey string, scope *SessionScope) {
	sessionKey = strings.TrimSpace(sessionKey)
	if !IsOpaqueSessionKey(sessionKey) || scope == nil || scope.Version != ScopeVersion {
		return
	}

	data, err := json.Marshal(scope)
	if err != nil {
		log.Printf("session: encode session scope: %v", err)
		return
	}
	ctx := context.Background()
	if err := b.store.UpsertSessionMeta(ctx, sessionKey, json.RawMessage(data), scope.ClientSessionID); err != nil {
		log.Printf("session: upsert session metadata: %v", err)
	}
}

// GetSessionScope reads structured scope metadata for a session key.
func (b *JSONLBackend) GetSessionScope(sessionKey string) *SessionScope {
	if !IsOpaqueSessionKey(sessionKey) {
		return nil
	}
	meta, err := b.store.GetSessionMeta(context.Background(), sessionKey)
	if err != nil {
		log.Printf("session: get session metadata: %v", err)
		return nil
	}
	if len(meta.Scope) == 0 {
		return nil
	}
	scope, err := decodeCurrentSessionScope(meta.Scope)
	if err != nil {
		log.Printf("session: decode session scope: %v", err)
		return nil
	}
	return scope
}

func decodeCurrentSessionScope(data json.RawMessage) (*SessionScope, error) {
	if err := validateSessionScopeJSON(data); err != nil {
		return nil, err
	}
	var scope SessionScope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&scope); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("session scope contains a trailing JSON value")
		}
		return nil, fmt.Errorf("session scope contains trailing data: %w", err)
	}
	if scope.Version != ScopeVersion {
		return nil, fmt.Errorf("unsupported session scope version %d", scope.Version)
	}
	if strings.TrimSpace(scope.AgentID) != "" {
		scope.AgentID = routing.NormalizeAgentID(scope.AgentID)
	}
	return CloneScope(&scope), nil
}

func validateSessionScopeJSON(data json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return errors.New("session scope must be a JSON object")
	}
	if err := validateSessionScopeObject(decoder, "scope", sessionScopeJSONFields); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("session scope contains a trailing JSON value")
		}
		return fmt.Errorf("session scope contains trailing data: %w", err)
	}
	return nil
}

func validateSessionScopeObject(
	decoder *json.Decoder,
	path string,
	allowedFields map[string]struct{},
) error {
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("session scope object %s has a non-string field name", path)
		}
		fieldPath := path + "." + key
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("session scope contains duplicate field %s", fieldPath)
		}
		seen[key] = struct{}{}
		if allowedFields != nil {
			if _, allowed := allowedFields[key]; !allowed {
				return fmt.Errorf("session scope contains unknown field %s", fieldPath)
			}
		}
		var childFields map[string]struct{}
		if path == "scope" && key == "epoch" {
			childFields = sessionEpochJSONFields
		}
		if err := validateSessionScopeValue(decoder, fieldPath, childFields); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	return nil
}

func validateSessionScopeValue(
	decoder *json.Decoder,
	path string,
	objectFields map[string]struct{},
) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		return validateSessionScopeObject(decoder, path, objectFields)
	case '[':
		for index := 0; decoder.More(); index++ {
			if err := validateSessionScopeValue(decoder, fmt.Sprintf("%s[%d]", path, index), nil); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return fmt.Errorf("session scope contains unexpected delimiter %q at %s", delimiter, path)
	}
}

// ClearSessionClientIDs removes accumulated frontend mappings without
// disturbing the session routing scope or canonical history.
func (b *JSONLBackend) ClearSessionClientIDs(sessionKey string) error {
	sessionKey = strings.TrimSpace(sessionKey)
	if !IsOpaqueSessionKey(sessionKey) {
		return fmt.Errorf("session: current opaque session key is required")
	}
	return b.store.ClearSessionClientIDs(context.Background(), sessionKey)
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
	return b.store.MutateHistory(ctx, sessionKey, mutate)
}

func (b *JSONLBackend) ReadTurnHistory(ctx context.Context, sessionKey string) ([]providers.Message, error) {
	if err := contextCause(ctx); err != nil {
		return nil, err
	}
	return b.store.GetHistory(ctx, sessionKey)
}

// ReadTurnHistoryPage reads a bounded canonical-history window.
func (b *JSONLBackend) ReadTurnHistoryPage(
	ctx context.Context,
	sessionKey string,
	request memory.HistoryPageRequest,
) (memory.HistoryPage, error) {
	if err := contextCause(ctx); err != nil {
		return memory.HistoryPage{}, err
	}
	return b.store.GetHistoryPage(ctx, sessionKey, request)
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
	msgs, err := b.ReadTurnHistory(context.Background(), key)
	if err != nil {
		log.Printf("session: get history: %v", err)
		return []providers.Message{}
	}
	return msgs
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

// ListCurrentAgentSessions returns persisted sessions owned by agentID with
// the current key and structured-scope contract. Historical, malformed, and
// differently owned metadata remains untouched but cannot enter recovery or
// background reconciliation.
func (b *JSONLBackend) ListCurrentAgentSessions(agentID string) []string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil
	}
	agentID = routing.NormalizeAgentID(agentID)

	keys := b.store.ListSessions()
	current := make([]string, 0, len(keys))
	for _, key := range keys {
		if !IsOpaqueSessionKey(key) {
			continue
		}
		meta, err := b.store.GetSessionMeta(context.Background(), key)
		if err != nil || len(meta.Scope) == 0 {
			continue
		}
		scope, decodeErr := decodeCurrentSessionScope(meta.Scope)
		if decodeErr != nil {
			continue
		}
		scopeAgentID := strings.TrimSpace(scope.AgentID)
		if scopeAgentID == "" || routing.NormalizeAgentID(scopeAgentID) != agentID {
			continue
		}
		current = append(current, key)
	}
	return current
}

// ListSessions returns every history owned by this store. Coding runtimes use
// a separate current identity contract and select their admitted thread at the
// agent composition boundary.
func (b *JSONLBackend) ListSessions() []string {
	return b.store.ListSessions()
}

// GetHistoryRevision returns the canonical history identity.
func (b *JSONLBackend) GetHistoryRevision(
	ctx context.Context,
	sessionKey string,
) (memory.HistoryRevision, error) {
	if err := contextCause(ctx); err != nil {
		return memory.HistoryRevision{}, err
	}
	return b.store.GetHistoryRevision(ctx, sessionKey)
}
