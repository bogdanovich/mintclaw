package seahorse

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// --- Helpers ---

func (s *Store) scanSummary(ctx context.Context, where string, args ...any) (*Summary, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT summary_id, conversation_id, kind, depth, content, token_count,
			earliest_at, latest_at, descendant_count, descendant_token_count,
			source_message_token_count, model, created_at
		 FROM summaries `+where, args...,
	)
	var sum Summary
	var kind, createdAt string
	var earliestAt, latestAt sql.NullString
	err := row.Scan(
		&sum.SummaryID, &sum.ConversationID, &kind, &sum.Depth, &sum.Content, &sum.TokenCount,
		&earliestAt, &latestAt, &sum.DescendantCount, &sum.DescendantTokenCount,
		&sum.SourceMessageTokenCount, &sum.Model, &createdAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("summary not found")
	}
	if err != nil {
		return nil, err
	}
	sum.Kind = SummaryKind(kind)
	sum.CreatedAt = parseSQLiteTime(createdAt)
	if earliestAt.Valid {
		t, _ := time.Parse(time.RFC3339, earliestAt.String)
		sum.EarliestAt = &t
	}
	if latestAt.Valid {
		t, _ := time.Parse(time.RFC3339, latestAt.String)
		sum.LatestAt = &t
	}
	return &sum, nil
}

func (s *Store) scanSummaries(rows *sql.Rows) ([]Summary, error) {
	var summaries []Summary
	for rows.Next() {
		var sum Summary
		var kind, createdAt string
		var earliestAt, latestAt sql.NullString
		err := rows.Scan(
			&sum.SummaryID, &sum.ConversationID, &kind, &sum.Depth, &sum.Content, &sum.TokenCount,
			&earliestAt, &latestAt, &sum.DescendantCount, &sum.DescendantTokenCount,
			&sum.SourceMessageTokenCount, &sum.Model, &createdAt,
		)
		if err != nil {
			return nil, err
		}
		sum.Kind = SummaryKind(kind)
		sum.CreatedAt = parseSQLiteTime(createdAt)
		if earliestAt.Valid {
			t, _ := time.Parse(time.RFC3339, earliestAt.String)
			sum.EarliestAt = &t
		}
		if latestAt.Valid {
			t, _ := time.Parse(time.RFC3339, latestAt.String)
			sum.LatestAt = &t
		}
		summaries = append(summaries, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return summaries, nil
}

func generateSummaryID(content string, t time.Time) string {
	return fmt.Sprintf("sum_%x", t.UnixNano())
}

func isUniqueViolation(err error) bool {
	return err != nil && (contains(err.Error(), "UNIQUE constraint failed") ||
		contains(err.Error(), "constraint failed"))
}

func normalizeMessageCreatedAt(createdAt time.Time) time.Time {
	if createdAt.IsZero() {
		return time.Time{}
	}
	return createdAt.UTC().Truncate(time.Second)
}

func formatSQLiteTime(t time.Time) string {
	return normalizeMessageCreatedAt(t).Format(sqliteTimeLayout)
}

func parseSQLiteTime(raw string) time.Time {
	parsed, _ := time.Parse(sqliteTimeLayout, raw)
	return parsed
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchSubstring(s, sub)
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func nullInt64(n int64) sql.NullInt64 {
	return sql.NullInt64{Int64: n, Valid: n != 0}
}
