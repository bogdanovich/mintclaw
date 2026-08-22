package memory

import (
	"context"
	"encoding/json"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

// Store defines an interface for persistent session storage.
// Each method is an atomic operation — there is no separate Save() call.
type Store interface {
	// AddMessage appends a simple text message to a session.
	AddMessage(ctx context.Context, sessionKey, role, content string) error

	// AddFullMessage appends a complete message (with tool calls, etc.) to a session.
	AddFullMessage(ctx context.Context, sessionKey string, msg providers.Message) error

	// GetHistory returns all messages for a session in insertion order.
	// Returns an empty slice (not nil) if the session does not exist.
	GetHistory(ctx context.Context, sessionKey string) ([]providers.Message, error)

	// GetSummary returns the conversation summary for a session.
	// Returns an empty string if no summary exists.
	GetSummary(ctx context.Context, sessionKey string) (string, error)

	// SetSummary updates the conversation summary for a session.
	SetSummary(ctx context.Context, sessionKey, summary string) error

	// TruncateHistory removes all but the last keepLast messages from a session.
	// If keepLast <= 0, all messages are removed.
	TruncateHistory(ctx context.Context, sessionKey string, keepLast int) error

	// SetHistory replaces all messages in a session with the provided history.
	SetHistory(ctx context.Context, sessionKey string, history []providers.Message) error

	// Compact reclaims storage by physically removing logically truncated
	// data. Backends that do not accumulate dead data may return nil.
	Compact(ctx context.Context, sessionKey string) error

	// ListSessions returns all known session keys.
	ListSessions() []string

	// Close releases any resources held by the store.
	Close() error

	// GetHistoryRevision returns a cheap durable identity for the visible history.
	GetHistoryRevision(ctx context.Context, sessionKey string) (HistoryRevision, error)

	// GetHistoryPage reads a bounded canonical-history window.
	GetHistoryPage(ctx context.Context, sessionKey string, request HistoryPageRequest) (HistoryPage, error)

	// MutateHistory atomically derives and persists a replacement from the
	// latest canonical history.
	MutateHistory(
		ctx context.Context,
		sessionKey string,
		mutate func([]providers.Message) ([]providers.Message, bool, error),
	) (bool, error)

	// GetSessionMeta and UpsertSessionMeta own current structured metadata.
	GetSessionMeta(ctx context.Context, sessionKey string) (SessionMeta, error)
	UpsertSessionMeta(ctx context.Context, sessionKey string, scope json.RawMessage, clientSessionID string) error
}

// HistoryRevision identifies the canonical visible history for a session.
// Dirty is durable evidence that a multi-file history mutation did not finish
// and consumers must rebuild from the canonical store before trusting a cache.
type HistoryRevision struct {
	Revision  uint64
	Count     int
	Skip      int
	Dirty     bool
	FileSize  int64
	ModTimeNS int64
}

// HistoryPageRequest selects a bounded canonical-history window. Before is an
// exclusive visible-message index; a negative value selects the current end.
type HistoryPageRequest struct {
	Before int
	Limit  int
	Cursor *HistoryCursor
}

// HistoryCursor binds paging to an immutable canonical prefix. Appends after
// Total are allowed; replacement, truncation, or reordering makes it stale.
type HistoryCursor struct {
	Total  int
	Digest string
}

// HistoryPage is a stable visible-message window at one history revision.
type HistoryPage struct {
	Messages []providers.Message
	Revision HistoryRevision
	Cursor   HistoryCursor
	Start    int
	End      int
	Total    int
	HasOlder bool
	HasNewer bool
}
