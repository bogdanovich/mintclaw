package seahorse

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// AddMessage appends a message to a conversation.
func (s *Store) AddMessage(ctx context.Context, convID int64, role, content string, tokenCount int) (*Message, error) {
	return s.AddMessageWithReasoning(ctx, convID, role, content, "", "", tokenCount, time.Time{})
}

// AddMessageWithReasoning appends a message with reasoning content to a conversation.
func (s *Store) AddMessageWithReasoning(
	ctx context.Context,
	convID int64,
	role, content, modelName, reasoningContent string,
	tokenCount int,
	createdAt time.Time,
) (*Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	added, err := addMessageTx(ctx, tx, convID, Message{
		Role:             role,
		Content:          content,
		ModelName:        modelName,
		ReasoningContent: reasoningContent,
		TokenCount:       tokenCount,
		CreatedAt:        createdAt,
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return added, nil
}

// partsToReadableContent derives a readable text summary from message parts.
// This ensures FTS5 indexing and summary formatting can access tool call information.
func partsToReadableContent(parts []MessagePart) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteString("\n")
		}
		switch p.Type {
		case "text":
			b.WriteString(p.Text)
		case "tool_use":
			fmt.Fprintf(&b, "[tool_use: %s, args: %s]", p.Name, p.Arguments)
		case "tool_result":
			fmt.Fprintf(&b, "[tool_result for %s: %s]", p.ToolCallID, p.Text)
		case "media":
			fmt.Fprintf(&b, "[media: %s (%s)]", p.MediaURI, p.MimeType)
		default:
			if p.Text != "" {
				b.WriteString(p.Text)
			}
		}
	}
	return b.String()
}

// AddMessageWithParts adds a message with structured parts.
func (s *Store) AddMessageWithParts(
	ctx context.Context,
	convID int64,
	role string,
	parts []MessagePart,
	tokenCount int,
) (*Message, error) {
	return s.AddMessageWithPartsAndReasoning(ctx, convID, role, parts, "", "", tokenCount, time.Time{})
}

// AddMessageWithPartsAndReasoning adds a message with structured parts and reasoning content.
func (s *Store) AddMessageWithPartsAndReasoning(
	ctx context.Context,
	convID int64,
	role string,
	parts []MessagePart,
	modelName string,
	reasoningContent string,
	tokenCount int,
	createdAt time.Time,
) (*Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	added, err := addMessageTx(ctx, tx, convID, Message{
		Role:             role,
		Parts:            parts,
		ModelName:        modelName,
		ReasoningContent: reasoningContent,
		TokenCount:       tokenCount,
		CreatedAt:        createdAt,
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return added, nil
}

// appendMessages writes messages, parts, and context entries in one transaction.
func (s *Store) appendMessages(ctx context.Context, convID int64, messages []Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.appendMessagesTx(ctx, tx, convID, messages); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// replaceConversationMessages atomically replaces all derived conversation state.
func (s *Store) replaceConversationMessages(ctx context.Context, convID int64, messages []Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := clearConversationTx(ctx, tx, convID); err != nil {
		return err
	}
	if err := s.appendMessagesTx(ctx, tx, convID, messages); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s *Store) appendMessagesTx(ctx context.Context, tx *sql.Tx, convID int64, messages []Message) error {
	if len(messages) == 0 {
		return nil
	}

	maxOrdinal, err := s.GetMaxOrdinalTx(ctx, tx, convID)
	if err != nil {
		return err
	}
	ordinal := maxOrdinal + OrdinalStep
	for i := range messages {
		added, err := addMessageTx(ctx, tx, convID, messages[i])
		if err != nil {
			return fmt.Errorf("add message %d: %w", i, err)
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO context_items (
				conversation_id, ordinal, item_type, message_id, token_count
			) VALUES (?, ?, 'message', ?, ?)`,
			convID,
			ordinal,
			added.ID,
			added.TokenCount,
		); err != nil {
			return fmt.Errorf("append message context %d: %w", i, err)
		}
		ordinal += OrdinalStep
	}
	return nil
}

func addMessageTx(ctx context.Context, tx *sql.Tx, convID int64, message Message) (*Message, error) {
	storedCreatedAt := normalizeMessageCreatedAt(message.CreatedAt)
	if storedCreatedAt.IsZero() {
		storedCreatedAt = normalizeMessageCreatedAt(time.Now())
	}
	content := message.Content
	if len(message.Parts) > 0 {
		content = partsToReadableContent(message.Parts)
	}

	result, err := tx.ExecContext(
		ctx,
		`INSERT INTO messages (
			conversation_id, role, content, model_name, reasoning_content, token_count, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		convID,
		message.Role,
		content,
		message.ModelName,
		message.ReasoningContent,
		message.TokenCount,
		formatSQLiteTime(storedCreatedAt),
	)
	if err != nil {
		return nil, fmt.Errorf("insert message: %w", err)
	}
	messageID, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get last insert id: %w", err)
	}

	parts := make([]MessagePart, len(message.Parts))
	for i, part := range message.Parts {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO message_parts (
				message_id, type, text, name, arguments, tool_call_id,
				tool_result_status, media_uri, mime_type, ordinal
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			messageID,
			part.Type,
			part.Text,
			part.Name,
			part.Arguments,
			part.ToolCallID,
			part.ToolResultStatus,
			part.MediaURI,
			part.MimeType,
			i,
		); err != nil {
			return nil, fmt.Errorf("add message part %d: %w", i, err)
		}
		part.MessageID = messageID
		parts[i] = part
	}

	message.ID = messageID
	message.ConversationID = convID
	message.Content = content
	message.CreatedAt = storedCreatedAt
	message.Parts = parts
	return &message, nil
}

// GetMessages retrieves messages for a conversation.
func (s *Store) GetMessages(ctx context.Context, convID int64, limit int, beforeID int64) ([]Message, error) {
	query := "SELECT message_id, conversation_id, role, content, model_name, reasoning_content, token_count, created_at FROM messages WHERE conversation_id = ?"
	args := []any{convID}
	if beforeID > 0 {
		query += " AND message_id < ?"
		args = append(args, beforeID)
	}
	query += " ORDER BY message_id ASC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var msgs []Message
	for rows.Next() {
		var msg Message
		var createdAt string
		if scanErr := rows.Scan(
			&msg.ID,
			&msg.ConversationID,
			&msg.Role,
			&msg.Content,
			&msg.ModelName,
			&msg.ReasoningContent,
			&msg.TokenCount,
			&createdAt,
		); scanErr != nil {
			return nil, scanErr
		}
		msg.CreatedAt = parseSQLiteTime(createdAt)
		msgs = append(msgs, msg)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}

	partsByMessage, err := s.loadMessagePartsBatch(ctx, msgs)
	if err != nil {
		return nil, err
	}
	for i := range msgs {
		msgs[i].Parts = partsByMessage[msgs[i].ID]
	}

	return msgs, nil
}

const messagePartsBatchSize = 500

func (s *Store) loadMessagePartsBatch(ctx context.Context, messages []Message) (map[int64][]MessagePart, error) {
	result := make(map[int64][]MessagePart, len(messages))
	for start := 0; start < len(messages); start += messagePartsBatchSize {
		end := min(start+messagePartsBatchSize, len(messages))
		if err := s.loadMessagePartsChunk(ctx, messages[start:end], result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Store) loadMessagePartsChunk(
	ctx context.Context,
	messages []Message,
	result map[int64][]MessagePart,
) error {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(messages)), ",")
	args := make([]any, 0, len(messages))
	for i := range messages {
		args = append(args, messages[i].ID)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT part_id, message_id, type, text,
		name, arguments, tool_call_id, tool_result_status, media_uri, mime_type, ordinal
		FROM message_parts WHERE message_id IN (`+placeholders+`)
		ORDER BY message_id, ordinal`, args...)
	if err != nil {
		return fmt.Errorf("load message parts batch: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var part MessagePart
		var ordinal int
		if scanErr := rows.Scan(&part.ID, &part.MessageID, &part.Type, &part.Text,
			&part.Name, &part.Arguments, &part.ToolCallID, &part.ToolResultStatus, &part.MediaURI,
			&part.MimeType, &ordinal); scanErr != nil {
			return scanErr
		}
		result[part.MessageID] = append(result[part.MessageID], part)
	}
	return rows.Err()
}

// GetMessageCount returns total message count for a conversation.
func (s *Store) GetMessageCount(ctx context.Context, convID int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		"SELECT count(*) FROM messages WHERE conversation_id = ?", convID,
	).Scan(&count)
	return count, err
}

// GetMessageByID retrieves a single message by ID.
func (s *Store) GetMessageByID(ctx context.Context, messageID int64) (*Message, error) {
	var msg Message
	var createdAt string
	err := s.db.QueryRowContext(
		ctx,
		"SELECT message_id, conversation_id, role, content, model_name, reasoning_content, token_count, created_at FROM messages WHERE message_id = ?",
		messageID,
	).Scan(&msg.ID, &msg.ConversationID, &msg.Role, &msg.Content, &msg.ModelName, &msg.ReasoningContent, &msg.TokenCount, &createdAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("message %d not found", messageID)
	}
	if err != nil {
		return nil, err
	}
	msg.CreatedAt = parseSQLiteTime(createdAt)
	msg.Parts, _ = s.loadMessageParts(ctx, msg.ID)
	return &msg, nil
}

func (s *Store) loadMessageParts(ctx context.Context, msgID int64) ([]MessagePart, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT part_id, message_id, type, text, name, arguments, tool_call_id,
		        tool_result_status, media_uri, mime_type
		 FROM message_parts WHERE message_id = ? ORDER BY ordinal`,
		msgID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var parts []MessagePart
	for rows.Next() {
		var p MessagePart
		if err := rows.Scan(&p.ID, &p.MessageID, &p.Type, &p.Text, &p.Name, &p.Arguments,
			&p.ToolCallID, &p.ToolResultStatus, &p.MediaURI, &p.MimeType); err != nil {
			return nil, err
		}
		parts = append(parts, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return parts, nil
}

// --- Summary Operations ---
