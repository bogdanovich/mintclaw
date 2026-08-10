package seahorse

import (
	"context"
	"database/sql"
	"strings"
)

// SearchSummaries performs full-text search on summaries.
func (s *Store) SearchSummaries(ctx context.Context, input SearchInput) ([]SearchResult, error) {
	// "like" → LIKE search, anything else (including "full_text" or empty) → FTS5
	if input.Mode == "like" {
		return s.searchSummariesLike(ctx, input)
	}
	return s.searchSummariesFTS(ctx, input)
}

func (s *Store) searchSummariesFTS(ctx context.Context, input SearchInput) ([]SearchResult, error) {
	sanitized := SanitizeFTS5Query(input.Pattern)
	if sanitized == "" {
		return nil, nil
	}

	// Build WHERE clause for filters (used in both count and data queries)
	whereClauses := []string{"summaries_fts MATCH ?"}
	args := []any{sanitized}

	if input.ConversationID > 0 {
		whereClauses = append(whereClauses, "s.conversation_id = ?")
		args = append(args, input.ConversationID)
	} else {
		whereClauses, args = appendConversationIDsWhere(
			whereClauses,
			args,
			"s.conversation_id",
			input.ConversationIDs,
		)
	}

	if input.Since != nil {
		whereClauses = append(whereClauses, "s.created_at >= ?")
		args = append(args, input.Since.Format("2006-01-02 15:04:05"))
	}
	if input.Before != nil {
		whereClauses = append(whereClauses, "s.created_at < ?")
		args = append(args, input.Before.Format("2006-01-02 15:04:05"))
	}

	whereStr := strings.Join(whereClauses, " AND ")

	// First, get total count (bm25 conflicts with window functions in FTS5)
	countQuery := `SELECT COUNT(*) FROM summaries_fts fts
		JOIN summaries s ON s.summary_id = fts.summary_id
		WHERE ` + whereStr
	var totalCount int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, err
	}

	// Then, get actual results with bm25 ranking
	dataQuery := `SELECT s.summary_id, s.conversation_id, s.kind, s.content, s.created_at, bm25(summaries_fts) as rank
		FROM summaries_fts fts
		JOIN summaries s ON s.summary_id = fts.summary_id
		WHERE ` + whereStr + ` ORDER BY rank`

	dataArgs := append([]any{}, args...) // copy args
	if input.Limit > 0 {
		dataQuery += " LIMIT ?"
		dataArgs = append(dataArgs, input.Limit)
	}

	rows, err := s.db.QueryContext(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	results, err := s.scanSearchResults(rows, true)
	if err != nil {
		return nil, err
	}

	// Set total count on all results
	for i := range results {
		results[i].TotalCount = totalCount
	}
	return results, nil
}

// buildLikeQuery appends conversation/time filters and limit to a LIKE query.
// Note: role filtering is NOT applied here since summaries don't have role column.
// Use buildMessagesLikeQuery for message searches that need role filtering.
func buildLikeQuery(query string, args []any, input SearchInput) (string, []any) {
	if input.ConversationID > 0 {
		query += " AND conversation_id = ?"
		args = append(args, input.ConversationID)
	} else {
		query, args = appendConversationIDsFilter(query, args, "conversation_id", input.ConversationIDs)
	}
	if input.Since != nil {
		query += " AND created_at >= ?"
		args = append(args, input.Since.Format("2006-01-02 15:04:05"))
	}
	if input.Before != nil {
		query += " AND created_at < ?"
		args = append(args, input.Before.Format("2006-01-02 15:04:05"))
	}
	// Order by newest first for LIKE mode
	query += " ORDER BY created_at DESC"
	if input.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, input.Limit)
	}
	return query, args
}

func appendConversationIDsFilter(
	query string,
	args []any,
	column string,
	conversationIDs []int64,
) (string, []any) {
	if len(conversationIDs) == 0 {
		return query + " AND 1 = 0", args
	}
	placeholders := make([]string, len(conversationIDs))
	for i, id := range conversationIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	return query + " AND " + column + " IN (" + strings.Join(placeholders, ",") + ")", args
}

// buildMessagesLikeQuery is like buildLikeQuery but adds role filtering for messages.
func buildMessagesLikeQuery(query string, args []any, input SearchInput) (string, []any) {
	if input.Role != "" {
		query += " AND role = ?"
		args = append(args, input.Role)
	}
	return buildLikeQuery(query, args, input)
}

func (s *Store) searchSummariesLike(ctx context.Context, input SearchInput) ([]SearchResult, error) {
	query := `SELECT summary_id, conversation_id, kind, content, created_at, COUNT(*) OVER() as total_count
		FROM summaries WHERE content LIKE ?`
	args := []any{"%" + input.Pattern + "%"}
	query, args = buildLikeQuery(query, args, input)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return s.scanSearchResults(rows, false)
}

func (s *Store) scanSearchResults(rows *sql.Rows, withRank bool) ([]SearchResult, error) {
	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var createdAt string
		var kind string
		if withRank {
			// FTS5 mode: no TotalCount in query (set by caller after COUNT)
			if err := rows.Scan(&r.SummaryID, &r.ConversationID, &kind, &r.Content, &createdAt, &r.Rank); err != nil {
				return nil, err
			}
		} else {
			// LIKE mode: TotalCount from window function
			if err := rows.Scan(&r.SummaryID, &r.ConversationID, &kind,
				&r.Content, &createdAt, &r.TotalCount); err != nil {
				return nil, err
			}
		}
		r.Kind = SummaryKind(kind)
		r.CreatedAt = parseSQLiteTime(createdAt)
		results = append(results, r)
	}
	return results, nil
}

// SearchMessages performs full-text or regex search on messages.
func (s *Store) SearchMessages(ctx context.Context, input SearchInput) ([]SearchResult, error) {
	// Try FTS5 first for full-text mode
	if input.Mode == "" || input.Mode == "full_text" {
		results, err := s.searchMessagesFTS(ctx, input)
		if err == nil && len(results) > 0 {
			return results, nil
		}
		// Fall through to LIKE
	}

	return s.searchMessagesLike(ctx, input)
}

func (s *Store) searchMessagesFTS(ctx context.Context, input SearchInput) ([]SearchResult, error) {
	sanitized := SanitizeFTS5Query(input.Pattern)
	if sanitized == "" {
		return nil, nil
	}

	// Build WHERE clause for filters (used in both count and data queries)
	whereClauses := []string{"messages_fts MATCH ?"}
	args := []any{sanitized}

	if input.ConversationID > 0 {
		whereClauses = append(whereClauses, "m.conversation_id = ?")
		args = append(args, input.ConversationID)
	} else {
		whereClauses, args = appendConversationIDsWhere(
			whereClauses,
			args,
			"m.conversation_id",
			input.ConversationIDs,
		)
	}

	if input.Role != "" {
		whereClauses = append(whereClauses, "m.role = ?")
		args = append(args, input.Role)
	}

	if input.Since != nil {
		whereClauses = append(whereClauses, "m.created_at >= ?")
		args = append(args, input.Since.Format("2006-01-02 15:04:05"))
	}
	if input.Before != nil {
		whereClauses = append(whereClauses, "m.created_at < ?")
		args = append(args, input.Before.Format("2006-01-02 15:04:05"))
	}

	whereStr := strings.Join(whereClauses, " AND ")

	// First, get total count (bm25 conflicts with window functions in FTS5)
	countQuery := `SELECT COUNT(*) FROM messages_fts f
		JOIN messages m ON f.message_id = m.message_id
		WHERE ` + whereStr
	var totalCount int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return nil, err
	}

	// Then, get actual results with bm25 ranking
	dataQuery := `SELECT m.message_id, m.conversation_id, m.role, m.content, m.created_at, bm25(messages_fts) as rank
		FROM messages_fts f
		JOIN messages m ON f.message_id = m.message_id
		WHERE ` + whereStr + ` ORDER BY rank`

	dataArgs := append([]any{}, args...) // copy args
	if input.Limit > 0 {
		dataQuery += " LIMIT ?"
		dataArgs = append(dataArgs, input.Limit)
	}

	rows, err := s.db.QueryContext(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	results, err := s.scanMessageSearchResults(rows, true)
	if err != nil {
		return nil, err
	}

	// Set total count on all results
	for i := range results {
		results[i].TotalCount = totalCount
	}
	return results, nil
}

func appendConversationIDsWhere(
	where []string,
	args []any,
	column string,
	conversationIDs []int64,
) ([]string, []any) {
	if len(conversationIDs) == 0 {
		return append(where, "1 = 0"), args
	}
	placeholders := make([]string, len(conversationIDs))
	for i, id := range conversationIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	return append(where, column+" IN ("+strings.Join(placeholders, ",")+")"), args
}

func (s *Store) searchMessagesLike(ctx context.Context, input SearchInput) ([]SearchResult, error) {
	query := `SELECT message_id, conversation_id, role, content, created_at, COUNT(*) OVER() as total_count
		FROM messages WHERE content LIKE ?`
	args := []any{"%" + input.Pattern + "%"}
	query, args = buildMessagesLikeQuery(query, args, input)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return s.scanMessageSearchResults(rows, false)
}

func (s *Store) scanMessageSearchResults(rows *sql.Rows, withRank bool) ([]SearchResult, error) {
	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var createdAt string
		var content string
		if withRank {
			// FTS5 mode: no TotalCount in query (set by caller after COUNT)
			if err := rows.Scan(&r.MessageID, &r.ConversationID, &r.Role, &content, &createdAt, &r.Rank); err != nil {
				return nil, err
			}
		} else {
			// LIKE mode: TotalCount from window function
			if err := rows.Scan(&r.MessageID, &r.ConversationID, &r.Role, &content,
				&createdAt, &r.TotalCount); err != nil {
				return nil, err
			}
		}
		r.Snippet = content
		r.CreatedAt = parseSQLiteTime(createdAt)
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
