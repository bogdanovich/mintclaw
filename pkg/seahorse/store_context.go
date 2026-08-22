package seahorse

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// GetContextItems retrieves context items for a conversation, ordered by ordinal.
func (s *Store) GetContextItems(ctx context.Context, convID int64) ([]ContextItem, error) {
	rows, err := s.db.QueryContext(
		ctx,
		"SELECT ordinal, item_type, summary_id, message_id, token_count, created_at FROM context_items WHERE conversation_id = ? ORDER BY ordinal",
		convID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var items []ContextItem
	for rows.Next() {
		var item ContextItem
		var summaryID sql.NullString
		var messageID sql.NullInt64
		var createdAt sql.NullString
		if err := rows.Scan(
			&item.Ordinal,
			&item.ItemType,
			&summaryID,
			&messageID,
			&item.TokenCount,
			&createdAt,
		); err != nil {
			return nil, err
		}
		item.ConversationID = convID
		if summaryID.Valid {
			item.SummaryID = summaryID.String
		}
		if messageID.Valid {
			item.MessageID = messageID.Int64
		}
		if createdAt.Valid {
			item.CreatedAt = parseSQLiteTime(createdAt.String)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// UpsertContextItems replaces all context items for a conversation.
func (s *Store) UpsertContextItems(ctx context.Context, convID int64, items []ContextItem) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, "DELETE FROM context_items WHERE conversation_id = ?", convID)
	if err != nil {
		return err
	}

	for _, item := range items {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO context_items (conversation_id, ordinal, item_type, summary_id, message_id, token_count)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			convID, item.Ordinal, item.ItemType,
			nullString(item.SummaryID), nullInt64(item.MessageID),
			item.TokenCount,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClearContextItems removes all context items for a conversation.
func (s *Store) ClearContextItems(ctx context.Context, convID int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM context_items WHERE conversation_id = ?", convID)
	return err
}

// ClearConversation removes all data for a conversation from all tables.
// Deletes context_items, summary_messages, summary_parents (via subquery), summaries,
// message_parts, and messages. FTS entries are handled automatically by triggers.
// Uses a transaction for atomicity.
func (s *Store) ClearConversation(ctx context.Context, convID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := clearConversationTx(ctx, tx, convID); err != nil {
		return err
	}
	return tx.Commit()
}

func clearConversationTx(ctx context.Context, tx *sql.Tx, convID int64) error {
	// Delete in child→parent order. FTS tables (messages_fts, summaries_fts) are
	// kept in sync by DELETE triggers, so we just delete from the parent tables.

	if _, err := tx.ExecContext(ctx,
		"DELETE FROM context_items WHERE conversation_id = ?", convID); err != nil {
		return fmt.Errorf("context_items: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM summary_messages WHERE summary_id IN (
			SELECT summary_id FROM summaries WHERE conversation_id = ?
		)`, convID); err != nil {
		return fmt.Errorf("summary_messages: %w", err)
	}
	// Note: summary_parents has no convID column; delete via subquery on summaries
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM summary_parents WHERE summary_id IN (
			SELECT summary_id FROM summaries WHERE conversation_id = ?
		) OR parent_summary_id IN (
			SELECT summary_id FROM summaries WHERE conversation_id = ?
		)`, convID, convID); err != nil {
		return fmt.Errorf("summary_parents: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM summaries WHERE conversation_id = ?", convID); err != nil {
		return fmt.Errorf("summaries: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM message_parts WHERE message_id IN (
			SELECT message_id FROM messages WHERE conversation_id = ?
		)`, convID); err != nil {
		return fmt.Errorf("message_parts: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM messages WHERE conversation_id = ?", convID); err != nil {
		return fmt.Errorf("messages: %w", err)
	}

	return nil
}

// AppendContextMessage appends a single message to context_items at next ordinal.
func (s *Store) AppendContextMessage(ctx context.Context, convID int64, messageID int64) error {
	return s.appendContextItems(ctx, convID, []ContextItem{
		{ItemType: "message", MessageID: messageID},
	})
}

// AppendContextMessages bulk-appends messages to context_items.
func (s *Store) AppendContextMessages(ctx context.Context, convID int64, messageIDs []int64) error {
	items := make([]ContextItem, len(messageIDs))
	for i, id := range messageIDs {
		items[i] = ContextItem{ItemType: "message", MessageID: id}
	}
	return s.appendContextItems(ctx, convID, items)
}

// AppendContextSummary appends a summary to context_items at next ordinal.
func (s *Store) AppendContextSummary(ctx context.Context, convID int64, summaryID string) error {
	return s.appendContextItems(ctx, convID, []ContextItem{
		{ItemType: "summary", SummaryID: summaryID},
	})
}

func (s *Store) appendContextItems(ctx context.Context, convID int64, items []ContextItem) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	maxOrd, err := s.GetMaxOrdinalTx(ctx, tx, convID)
	if err != nil {
		return err
	}

	ordinal := maxOrd + OrdinalStep
	for _, item := range items {
		item.ConversationID = convID
		item.Ordinal = ordinal

		// Resolve token count if not set
		tokenCount := item.TokenCount
		if tokenCount == 0 {
			tokenCount = s.resolveItemTokenCountTx(ctx, tx, item)
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO context_items (conversation_id, ordinal, item_type, summary_id, message_id, token_count)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			convID, ordinal, item.ItemType,
			nullString(item.SummaryID), nullInt64(item.MessageID),
			tokenCount,
		)
		if err != nil {
			return err
		}
		ordinal += OrdinalStep
	}
	return tx.Commit()
}

// resolveItemTokenCountTx looks up token count within a transaction.
func (s *Store) resolveItemTokenCountTx(ctx context.Context, tx *sql.Tx, item ContextItem) int {
	if item.ItemType == "message" && item.MessageID > 0 {
		var tc int
		err := tx.QueryRowContext(ctx,
			"SELECT token_count FROM messages WHERE message_id = ?", item.MessageID,
		).Scan(&tc)
		if err == nil {
			return tc
		}
	}
	if item.ItemType == "summary" && item.SummaryID != "" {
		var tc int
		err := tx.QueryRowContext(ctx,
			"SELECT token_count FROM summaries WHERE summary_id = ?", item.SummaryID,
		).Scan(&tc)
		if err == nil {
			return tc
		}
	}
	return 0
}

// ReplaceContextRangeWithSummary atomically replaces a range of context items with a summary.
// If ordinal gap is exhausted, triggers resequencing (spec lines 1204-1209).
func (s *Store) ReplaceContextRangeWithSummary(
	ctx context.Context,
	convID int64,
	startOrdinal, endOrdinal int,
	summaryID string,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Delete the range
	_, err = tx.ExecContext(ctx,
		"DELETE FROM context_items WHERE conversation_id = ? AND ordinal >= ? AND ordinal <= ?",
		convID, startOrdinal, endOrdinal,
	)
	if err != nil {
		return err
	}

	// Insert summary at midpoint of replaced range
	midpoint := (startOrdinal + endOrdinal) / 2

	// Check if midpoint conflicts with existing ordinal
	var conflict bool
	var existingOrd int
	err = tx.QueryRowContext(ctx,
		"SELECT ordinal FROM context_items WHERE conversation_id = ? AND ordinal = ?",
		convID, midpoint,
	).Scan(&existingOrd)
	if err == nil {
		conflict = true
	}

	if conflict {
		// Gap exhausted, need resequence (spec lines 1204-1209)
		err = s.resequenceContextItemsTx(ctx, tx, convID, summaryID)
		if err != nil {
			return fmt.Errorf("resequence: %w", err)
		}
	} else {
		// Normal insert at midpoint with token_count from summary
		_, err = tx.ExecContext(ctx,
			`INSERT INTO context_items (conversation_id, ordinal, item_type, summary_id, token_count)
			 SELECT ?, ?, 'summary', ?, token_count FROM summaries WHERE summary_id = ?`,
			convID, midpoint, summaryID, summaryID,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ReplaceContextItemsWithSummary replaces specific context items (by summary_id) with a new summary.
// Use this when candidates are not contiguous in ordinal space to avoid deleting non-candidate items.
func (s *Store) ReplaceContextItemsWithSummary(
	ctx context.Context,
	convID int64,
	summaryIDs []string,
	newSummaryID string,
) error {
	if len(summaryIDs) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Find the ordinals of items to delete and calculate midpoint
	placeholders := make([]string, len(summaryIDs))
	args := make([]any, len(summaryIDs)+1)
	args[0] = convID
	for i, sid := range summaryIDs {
		placeholders[i] = "?"
		args[i+1] = sid
	}

	query := fmt.Sprintf(
		"SELECT ordinal FROM context_items WHERE conversation_id = ? AND summary_id IN (%s) ORDER BY ordinal",
		strings.Join(placeholders, ","),
	)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	var ordinals []int
	for rows.Next() {
		var ord int
		if scanErr := rows.Scan(&ord); scanErr != nil {
			return scanErr
		}
		ordinals = append(ordinals, ord)
	}
	if err = rows.Err(); err != nil {
		return err
	}

	if len(ordinals) == 0 {
		return nil
	}

	midpoint := (ordinals[0] + ordinals[len(ordinals)-1]) / 2

	// Delete the specific items by summary_id
	deleteQuery := fmt.Sprintf(
		"DELETE FROM context_items WHERE conversation_id = ? AND summary_id IN (%s)",
		strings.Join(placeholders, ","),
	)
	_, err = tx.ExecContext(ctx, deleteQuery, args...)
	if err != nil {
		return err
	}

	// Check if midpoint conflicts with existing ordinal
	var conflict bool
	var existingOrd int
	err = tx.QueryRowContext(ctx,
		"SELECT ordinal FROM context_items WHERE conversation_id = ? AND ordinal = ?",
		convID, midpoint,
	).Scan(&existingOrd)
	if err == nil {
		conflict = true
	}

	if conflict {
		// Gap exhausted, need resequence
		err = s.resequenceContextItemsTx(ctx, tx, convID, newSummaryID)
		if err != nil {
			return fmt.Errorf("resequence: %w", err)
		}
	} else {
		// Normal insert at midpoint
		_, err = tx.ExecContext(ctx,
			`INSERT INTO context_items (conversation_id, ordinal, item_type, summary_id, token_count)
			 SELECT ?, ?, 'summary', ?, token_count FROM summaries WHERE summary_id = ?`,
			convID, midpoint, newSummaryID, newSummaryID,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// resequenceContextItemsTx renumbers context_items with fresh OrdinalStep gaps.
// Uses temp negative ordinals to avoid PRIMARY KEY constraint violations (spec lines 1240-1247).
func (s *Store) resequenceContextItemsTx(ctx context.Context, tx *sql.Tx, convID int64, newSummaryID string) error {
	// Get all remaining items sorted by current ordinal
	rows, err := tx.QueryContext(
		ctx,
		"SELECT ordinal, item_type, summary_id, message_id, token_count FROM context_items WHERE conversation_id = ? ORDER BY ordinal",
		convID,
	)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	type item struct {
		ordinal    int
		itemType   string
		summaryID  string
		messageID  int64
		tokenCount int
	}
	var items []item
	for rows.Next() {
		var i item
		var sid sql.NullString
		var mid sql.NullInt64
		var scanErr error
		if scanErr = rows.Scan(&i.ordinal, &i.itemType, &sid, &mid, &i.tokenCount); scanErr != nil {
			return scanErr
		}
		if sid.Valid {
			i.summaryID = sid.String
		}
		if mid.Valid {
			i.messageID = mid.Int64
		}
		items = append(items, i)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return rowsErr
	}

	// Step 1: Move all items to temp negative ordinals
	tempOrd := -1
	for _, i := range items {
		_, execErr := tx.ExecContext(ctx,
			"UPDATE context_items SET ordinal = ? WHERE conversation_id = ? AND ordinal = ?",
			tempOrd, convID, i.ordinal,
		)
		if execErr != nil {
			return execErr
		}
		tempOrd--
	}

	// Step 2: Insert new summary at the end with positive ordinal
	// Include token_count from summaries table
	newOrd := (len(items) + 1) * OrdinalStep
	_, err = tx.ExecContext(ctx,
		`INSERT INTO context_items (conversation_id, ordinal, item_type, summary_id, token_count)
		 SELECT ?, ?, 'summary', ?, token_count FROM summaries WHERE summary_id = ?`,
		convID, newOrd, newSummaryID, newSummaryID,
	)
	if err != nil {
		return err
	}

	// Step 3: Update each temp item to its final positive ordinal
	// Use specific temp ordinal matching (not ordinal < 0) to avoid updating all items
	finalOrd := OrdinalStep
	tempOrd = -1 // Reset to first temp ordinal (already declared in Step 1)
	for range items {
		_, execErr := tx.ExecContext(ctx,
			"UPDATE context_items SET ordinal = ? WHERE conversation_id = ? AND ordinal = ?",
			finalOrd, convID, tempOrd,
		)
		if execErr != nil {
			return execErr
		}
		finalOrd += OrdinalStep
		tempOrd--
	}

	return nil
}

// GetContextTokenCount returns total token count for all items in context.
func (s *Store) GetContextTokenCount(ctx context.Context, convID int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(token_count), 0) FROM context_items WHERE conversation_id = ?",
		convID,
	).Scan(&count)
	return count, err
}

// GetContextTokenCounts returns stored message and summary token totals.
func (s *Store) GetContextTokenCounts(ctx context.Context, convID int64) (int, int, error) {
	var historyTokens, summaryTokens int
	err := s.db.QueryRowContext(ctx,
		`SELECT
			COALESCE(SUM(CASE WHEN item_type = 'message' THEN token_count ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN item_type = 'summary' THEN token_count ELSE 0 END), 0)
		 FROM context_items WHERE conversation_id = ?`,
		convID,
	).Scan(&historyTokens, &summaryTokens)
	if err != nil {
		return 0, 0, fmt.Errorf("get context token counts: %w", err)
	}
	return historyTokens, summaryTokens, nil
}

// GetMaxOrdinal returns the highest ordinal in context_items for a conversation.
func (s *Store) GetMaxOrdinal(ctx context.Context, convID int64) (int, error) {
	var maxOrd sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		"SELECT MAX(ordinal) FROM context_items WHERE conversation_id = ?",
		convID,
	).Scan(&maxOrd)
	if err != nil {
		return 0, err
	}
	if !maxOrd.Valid {
		return 0, nil
	}
	return int(maxOrd.Int64), nil
}

// GetMaxOrdinalTx returns the highest ordinal within a transaction.
func (s *Store) GetMaxOrdinalTx(ctx context.Context, tx *sql.Tx, convID int64) (int, error) {
	var maxOrd sql.NullInt64
	err := tx.QueryRowContext(ctx,
		"SELECT MAX(ordinal) FROM context_items WHERE conversation_id = ?",
		convID,
	).Scan(&maxOrd)
	if err != nil {
		return 0, err
	}
	if !maxOrd.Valid {
		return 0, nil
	}
	return int(maxOrd.Int64), nil
}

// GetDistinctDepthsInContext returns distinct depth levels of summaries currently in context.
// maxOrdinalExclusive filters out summaries with ordinal >= this value (0 = no filter).
func (s *Store) GetDistinctDepthsInContext(ctx context.Context, convID int64, maxOrdinalExclusive int) ([]int, error) {
	query := `SELECT DISTINCT s.depth
		FROM context_items ci
		JOIN summaries s ON s.summary_id = ci.summary_id
		WHERE ci.conversation_id = ? AND ci.item_type = 'summary'`
	args := []any{convID}

	if maxOrdinalExclusive > 0 {
		query += " AND ci.ordinal < ?"
		args = append(args, maxOrdinalExclusive)
	}

	query += " ORDER BY s.depth"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get distinct depths: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var depths []int
	for rows.Next() {
		var d int
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		depths = append(depths, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return depths, nil
}

// GetSummarySubtree returns all summaries in the subtree rooted at summaryID,
// including summaryID itself. Uses a recursive CTE to traverse the DAG.
func (s *Store) GetSummarySubtree(ctx context.Context, summaryID string) ([]SummarySubtreeNode, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT summary_id, 0 AS depth_from_root
			FROM summaries
			WHERE summary_id = ?
			UNION ALL
			SELECT sp.parent_summary_id, st.depth_from_root + 1
			FROM summary_parents sp
			JOIN subtree st ON sp.summary_id = st.summary_id
		)
		SELECT summary_id, depth_from_root FROM subtree`,
		summaryID,
	)
	if err != nil {
		return nil, fmt.Errorf("get summary subtree: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var nodes []SummarySubtreeNode
	for rows.Next() {
		var n SummarySubtreeNode
		if err := rows.Scan(&n.SummaryID, &n.DepthFromRoot); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nodes, nil
}

// --- Search Operations ---
