package seahorse

import (
	"context"
	"fmt"
	"time"
)

// GetSessionStatus returns status for a specific session.
func (s *Store) GetSessionStatus(ctx context.Context, sessionKey string) (*SessionStatus, error) {
	conv, err := s.GetConversationBySessionKey(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	if conv == nil {
		return nil, nil
	}

	msgCount, _ := s.GetMessageCount(ctx, conv.ConversationID)
	sumCount, _ := s.getSummaryCount(ctx, conv.ConversationID)
	tokenCount, _ := s.GetContextTokenCount(ctx, conv.ConversationID)

	oldest, newest, _ := s.getMessageTimeRange(ctx, conv.ConversationID)

	return &SessionStatus{
		SessionKey:     conv.SessionKey,
		ConversationID: conv.ConversationID,
		Messages:       msgCount,
		TotalTokens:    tokenCount,
		Summaries:      sumCount,
		OldestAt:       oldest,
		NewestAt:       newest,
	}, nil
}

// GetAllSessionStatuses returns status for all sessions.
func (s *Store) GetAllSessionStatuses(ctx context.Context) ([]SessionStatus, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT session_key FROM conversations")
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var sessionKeys []string
	for rows.Next() {
		var sessionKey string
		if err := rows.Scan(&sessionKey); err != nil {
			continue
		}
		sessionKeys = append(sessionKeys, sessionKey)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}

	statuses := make([]SessionStatus, 0, len(sessionKeys))
	for _, sessionKey := range sessionKeys {
		status, err := s.GetSessionStatus(ctx, sessionKey)
		if err != nil {
			continue
		}
		if status != nil {
			statuses = append(statuses, *status)
		}
	}
	return statuses, nil
}

func (s *Store) getSummaryCount(ctx context.Context, convID int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM summaries WHERE conversation_id = ?",
		convID,
	).Scan(&count)
	return count, err
}

func (s *Store) getMessageTimeRange(ctx context.Context, convID int64) (time.Time, time.Time, error) {
	var minTime, maxTime string
	err := s.db.QueryRowContext(ctx,
		"SELECT MIN(created_at), MAX(created_at) FROM messages WHERE conversation_id = ?",
		convID,
	).Scan(&minTime, &maxTime)
	if err != nil || minTime == "" {
		return time.Time{}, time.Time{}, err
	}
	oldest := parseSQLiteTime(minTime)
	newest := parseSQLiteTime(maxTime)
	return oldest, newest, nil
}

// --- Message Operations ---
