package seahorse

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CreateSummary creates a new summary and indexes it in FTS5.
func (s *Store) CreateSummary(ctx context.Context, input CreateSummaryInput) (*Summary, error) {
	// Generate summary ID
	now := time.Now().UTC()
	summaryID := generateSummaryID(input.Content, now)

	var earliestAt, latestAt sql.NullString
	if input.EarliestAt != nil {
		earliestAt = sql.NullString{String: input.EarliestAt.Format(time.RFC3339), Valid: true}
	}
	if input.LatestAt != nil {
		latestAt = sql.NullString{String: input.LatestAt.Format(time.RFC3339), Valid: true}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO summaries (summary_id, conversation_id, kind, depth, content, token_count,
			earliest_at, latest_at, descendant_count, descendant_token_count,
			source_message_token_count, model)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		summaryID, input.ConversationID, string(input.Kind), input.Depth,
		input.Content, input.TokenCount,
		earliestAt, latestAt,
		input.DescendantCount, input.DescendantTokenCount,
		input.SourceMessageTokens, input.Model,
	)
	if err != nil {
		return nil, fmt.Errorf("insert summary: %w", err)
	}

	// FTS trigger will fire automatically for summaries table insert

	// Link parent summaries (DAG edges) for condensed summaries
	for _, parentID := range input.ParentIDs {
		_, err = tx.ExecContext(ctx,
			"INSERT INTO summary_parents (summary_id, parent_summary_id) VALUES (?, ?)",
			summaryID, parentID,
		)
		if err != nil {
			return nil, fmt.Errorf("link parent %s: %w", parentID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return &Summary{
		SummaryID:               summaryID,
		ConversationID:          input.ConversationID,
		Kind:                    input.Kind,
		Depth:                   input.Depth,
		Content:                 input.Content,
		TokenCount:              input.TokenCount,
		EarliestAt:              input.EarliestAt,
		LatestAt:                input.LatestAt,
		DescendantCount:         input.DescendantCount,
		DescendantTokenCount:    input.DescendantTokenCount,
		SourceMessageTokenCount: input.SourceMessageTokens,
		Model:                   input.Model,
		CreatedAt:               now,
	}, nil
}

// GetSummary retrieves a summary by ID.
func (s *Store) GetSummary(ctx context.Context, summaryID string) (*Summary, error) {
	return s.scanSummary(ctx, "WHERE summary_id = ?", summaryID)
}

// GetSummariesByConversation retrieves all summaries for a conversation.
func (s *Store) GetSummariesByConversation(ctx context.Context, convID int64) ([]Summary, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT summary_id, conversation_id, kind, depth, content, token_count,
			earliest_at, latest_at, descendant_count, descendant_token_count,
			source_message_token_count, model, created_at
		 FROM summaries WHERE conversation_id = ? ORDER BY created_at`,
		convID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return s.scanSummaries(rows)
}

// GetSummaryChildren retrieves child summary IDs (summaries that list this summary as parent).
func (s *Store) GetSummaryChildren(ctx context.Context, summaryID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT summary_id FROM summary_parents WHERE parent_summary_id = ?",
		summaryID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// GetSummaryParents retrieves parent summaries (full objects) for a summary.
func (s *Store) GetSummaryParents(ctx context.Context, summaryID string) ([]Summary, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT s.summary_id, s.conversation_id, s.kind, s.depth, s.content, s.token_count,
			s.earliest_at, s.latest_at, s.descendant_count, s.descendant_token_count,
			s.source_message_token_count, s.model, s.created_at
		 FROM summary_parents sp
		 JOIN summaries s ON s.summary_id = sp.parent_summary_id
		 WHERE sp.summary_id = ?`,
		summaryID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return s.scanSummaries(rows)
}

// LinkSummaryToMessages links a leaf summary to its source messages.
func (s *Store) LinkSummaryToMessages(ctx context.Context, summaryID string, messageIDs []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for i, msgID := range messageIDs {
		_, err = tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO summary_messages (summary_id, message_id, ordinal) VALUES (?, ?, ?)",
			summaryID, msgID, i,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetSummarySourceMessages retrieves source messages for a summary.
func (s *Store) GetSummarySourceMessages(ctx context.Context, summaryID string) ([]Message, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT m.message_id, m.conversation_id, m.role, m.content, m.model_name, m.reasoning_content, m.token_count, m.created_at
		 FROM summary_messages sm
		 JOIN messages m ON m.message_id = sm.message_id
		 WHERE sm.summary_id = ?
		 ORDER BY sm.ordinal`,
		summaryID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var msgs []Message
	for rows.Next() {
		var msg Message
		var createdAt string
		if err := rows.Scan(
			&msg.ID,
			&msg.ConversationID,
			&msg.Role,
			&msg.Content,
			&msg.ModelName,
			&msg.ReasoningContent,
			&msg.TokenCount,
			&createdAt,
		); err != nil {
			return nil, err
		}
		msg.CreatedAt = parseSQLiteTime(createdAt)
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return msgs, nil
}

// GetRootSummaries retrieves root summaries (not children of any other summary).
func (s *Store) GetRootSummaries(ctx context.Context, convID int64) ([]Summary, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT s.summary_id, s.conversation_id, s.kind, s.depth, s.content, s.token_count,
			s.earliest_at, s.latest_at, s.descendant_count, s.descendant_token_count,
			s.source_message_token_count, s.model, s.created_at
		 FROM summaries s
		 WHERE s.conversation_id = ?
		 AND s.summary_id NOT IN (SELECT sp.parent_summary_id FROM summary_parents sp)
		 ORDER BY s.created_at`,
		convID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return s.scanSummaries(rows)
}

// --- Context Item Operations ---
