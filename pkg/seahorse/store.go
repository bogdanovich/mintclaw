package seahorse

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const sqliteTimeLayout = "2006-01-02 15:04:05"

// Store provides SQLite storage for seahorse.
type Store struct {
	db *sql.DB
}

func (s *Store) verifyIntegrity(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("verify store integrity: database is unavailable")
	}
	if ctx == nil {
		return fmt.Errorf("verify store integrity: context is required")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, "PRAGMA quick_check(1)")
	if err != nil {
		return fmt.Errorf("verify store integrity: %w", err)
	}
	defer func() { _ = rows.Close() }()
	found := false
	quickCheckFailed := false
	for rows.Next() {
		found = true
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("verify store integrity result: %w", err)
		}
		if result != "ok" {
			quickCheckFailed = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("verify store integrity: %w", err)
	}
	if !found {
		return fmt.Errorf("verify store integrity: SQLite quick check returned no result")
	}
	if quickCheckFailed {
		if err := s.verifyTablesReadable(ctx); err != nil {
			return fmt.Errorf("verify store integrity: %w", err)
		}
		return fmt.Errorf("verify store integrity: SQLite quick check failed")
	}
	return nil
}

func (s *Store) verifyTablesReadable(ctx context.Context) error {
	// quick_check reports corruption as result text. Probe one row from each
	// current table so reset remains authorized only by a typed SQLite error.
	names, err := s.derivedTableNames(ctx)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := s.verifyTableReadable(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) derivedTableNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type = 'table' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return names, nil
}

func (s *Store) verifyTableReadable(ctx context.Context, name string) error {
	identifier := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	rows, err := s.db.QueryContext(ctx, "SELECT * FROM "+identifier+" LIMIT 1")
	if err != nil {
		return fmt.Errorf("read derived table %q: %w", name, err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("inspect derived table %q: %w", name, err)
	}
	values := make([]sql.RawBytes, len(columns))
	destinations := make([]any, len(columns))
	for index := range values {
		destinations[index] = &values[index]
	}
	for rows.Next() {
		if err := rows.Scan(destinations...); err != nil {
			return fmt.Errorf("read derived table %q: %w", name, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read derived table %q: %w", name, err)
	}
	return nil
}

// ReconciliationState records which canonical history revision has been
// incorporated into Seahorse for a session.
type ReconciliationState struct {
	SessionKey       string
	SourceRevision   uint64
	SourceCount      int
	SourceSkip       int
	SourceFileSize   int64
	SourceModTimeNS  int64
	SchemaGeneration int
}

func (s *Store) GetReconciliationState(ctx context.Context, sessionKey string) (*ReconciliationState, error) {
	var state ReconciliationState
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT session_key, source_revision, source_count,
		source_skip, source_file_size, source_mod_time_ns, schema_generation
		FROM reconciliation_state WHERE session_key = ?`, sessionKey).Scan(
		&state.SessionKey, &revision, &state.SourceCount, &state.SourceSkip,
		&state.SourceFileSize, &state.SourceModTimeNS, &state.SchemaGeneration,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get reconciliation state: %w", err)
	}
	state.SourceRevision = uint64(revision)
	return &state, nil
}

func (s *Store) SetReconciliationState(ctx context.Context, state ReconciliationState) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO reconciliation_state (
		session_key, source_revision, source_count, source_skip, source_file_size,
		source_mod_time_ns, schema_generation, reconciled_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))
	ON CONFLICT(session_key) DO UPDATE SET
		source_revision = excluded.source_revision,
		source_count = excluded.source_count,
		source_skip = excluded.source_skip,
		source_file_size = excluded.source_file_size,
		source_mod_time_ns = excluded.source_mod_time_ns,
		schema_generation = excluded.schema_generation,
		reconciled_at = excluded.reconciled_at`,
		state.SessionKey, int64(state.SourceRevision), state.SourceCount, state.SourceSkip,
		state.SourceFileSize, state.SourceModTimeNS, state.SchemaGeneration,
	)
	if err != nil {
		return fmt.Errorf("set reconciliation state: %w", err)
	}
	return nil
}

// CreateSummaryInput holds parameters for creating a summary.
type CreateSummaryInput struct {
	ConversationID       int64
	Kind                 SummaryKind
	Depth                int
	Content              string
	TokenCount           int
	EarliestAt           *time.Time
	LatestAt             *time.Time
	DescendantCount      int
	DescendantTokenCount int
	SourceMessageTokens  int
	Model                string
	ParentIDs            []string // For condensed: child summary IDs being condensed
}

// --- Conversation Operations ---

// GetOrCreateConversation returns the conversation for a sessionKey, creating if needed.
func (s *Store) GetOrCreateConversation(ctx context.Context, sessionKey string) (*Conversation, error) {
	// Try to get first
	conv, err := s.GetConversationBySessionKey(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	if conv != nil {
		return conv, nil
	}

	// Create
	result, err := s.db.ExecContext(ctx,
		"INSERT INTO conversations (session_key) VALUES (?)",
		sessionKey,
	)
	if err != nil {
		// Race: another goroutine may have inserted
		if isUniqueViolation(err) {
			return s.GetConversationBySessionKey(ctx, sessionKey)
		}
		return nil, fmt.Errorf("create conversation: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get last insert id: %w", err)
	}
	return &Conversation{
		ConversationID: id,
		SessionKey:     sessionKey,
	}, nil
}

// GetConversationBySessionKey retrieves a conversation by session key.
func (s *Store) GetConversationBySessionKey(ctx context.Context, sessionKey string) (*Conversation, error) {
	var conv Conversation
	var createdAt, updatedAt string
	var routeScopeKey, agentID sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT conversation_id, session_key, route_scope_key, agent_id, created_at, updated_at
		 FROM conversations WHERE session_key = ?`,
		sessionKey,
	).Scan(
		&conv.ConversationID,
		&conv.SessionKey,
		&routeScopeKey,
		&agentID,
		&createdAt,
		&updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get conversation by session key: %w", err)
	}
	conv.CreatedAt = parseSQLiteTime(createdAt)
	conv.UpdatedAt = parseSQLiteTime(updatedAt)
	conv.RouteScopeKey = routeScopeKey.String
	conv.AgentID = agentID.String
	return &conv, nil
}

// SetConversationProvenance records trusted route metadata without overwriting
// an existing, conflicting identity.
func (s *Store) SetConversationProvenance(
	ctx context.Context,
	sessionKey,
	routeScopeKey,
	agentID string,
) error {
	sessionKey = strings.TrimSpace(sessionKey)
	routeScopeKey = strings.TrimSpace(routeScopeKey)
	agentID = strings.TrimSpace(agentID)
	if sessionKey == "" || routeScopeKey == "" || agentID == "" {
		return fmt.Errorf("session key, route scope key, and agent ID are required")
	}
	conv, err := s.GetOrCreateConversation(ctx, sessionKey)
	if err != nil {
		return err
	}
	if conv.RouteScopeKey != "" && conv.RouteScopeKey != routeScopeKey ||
		conv.AgentID != "" && conv.AgentID != agentID {
		return fmt.Errorf("conversation %q has conflicting route provenance", sessionKey)
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE conversations
		 SET route_scope_key = CASE
		       WHEN route_scope_key IS NULL OR route_scope_key = '' THEN ?
		       ELSE route_scope_key
		     END,
		     agent_id = CASE
		       WHEN agent_id IS NULL OR agent_id = '' THEN ?
		       ELSE agent_id
		     END
		 WHERE conversation_id = ?
		   AND (route_scope_key IS NULL OR route_scope_key = '' OR route_scope_key = ?)
		   AND (agent_id IS NULL OR agent_id = '' OR agent_id = ?)`,
		routeScopeKey,
		agentID,
		conv.ConversationID,
		routeScopeKey,
		agentID,
	)
	if err != nil {
		return fmt.Errorf("set conversation provenance: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check conversation provenance update: %w", err)
	}
	if rowsAffected == 0 && (conv.RouteScopeKey == "" || conv.AgentID == "") {
		current, getErr := s.GetConversationBySessionKey(ctx, sessionKey)
		if getErr != nil {
			return getErr
		}
		if current == nil || current.RouteScopeKey != routeScopeKey || current.AgentID != agentID {
			return fmt.Errorf("conversation %q has conflicting route provenance", sessionKey)
		}
	}
	return nil
}

func (s *Store) conversationIDsForRouteScope(
	ctx context.Context,
	routeScopeKey,
	agentID string,
) ([]int64, error) {
	return s.conversationIDs(ctx,
		"route_scope_key = ? AND agent_id = ?",
		strings.TrimSpace(routeScopeKey),
		strings.TrimSpace(agentID),
	)
}

func (s *Store) conversationIDsForAgent(ctx context.Context, agentID string) ([]int64, error) {
	return s.conversationIDs(ctx, "agent_id = ?", strings.TrimSpace(agentID))
}

func (s *Store) conversationIDs(ctx context.Context, clause string, args ...any) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT conversation_id FROM conversations WHERE "+clause+" ORDER BY conversation_id",
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list scoped conversations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan scoped conversation: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scoped conversations: %w", err)
	}
	return ids, nil
}
