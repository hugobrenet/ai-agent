package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/hugobrenet/opensvc-ai-agent/internal/conversation"
)

// ListMessages reads only display text. Raw tool payloads are deliberately not
// selected, and the model's bounded-history window is not applied to display.
func (s *Store) ListMessages(ctx context.Context, owner conversation.Owner, id string, query conversation.MessageQuery) (conversation.MessagePage, error) {
	if err := validateOwnerAndID(owner, id); err != nil {
		return conversation.MessagePage{}, err
	}
	limit, beforeTurn, beforeMessage, err := query.Bounds()
	if err != nil {
		return conversation.MessagePage{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conversation.MessagePage{}, fmt.Errorf("begin list messages transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireConversation(ctx, tx, owner, id); err != nil {
		return conversation.MessagePage{}, err
	}
	rows, err := tx.QueryContext(ctx, `
SELECT t.sequence, t.id, m.sequence, m.role,
       CASE WHEN length(CAST(m.text AS BLOB)) <= ? THEN m.text ELSE NULL END,
       t.started_at, t.completed_at
FROM messages AS m
JOIN turns AS t ON t.id = m.turn_id
WHERE t.conversation_id = ? AND t.status = 'completed'
  AND (m.role = 'user' OR (m.role = 'assistant' AND m.text <> ''))
  AND (t.sequence < ? OR (t.sequence = ? AND m.sequence < ?))
ORDER BY t.sequence DESC, m.sequence DESC
LIMIT ?`, conversation.MaxMessagePageBytes, id, beforeTurn, beforeTurn, beforeMessage, limit+1)
	if err != nil {
		return conversation.MessagePage{}, fmt.Errorf("query display messages: %w", err)
	}
	defer rows.Close()
	page := conversation.MessagePage{Messages: make([]conversation.DisplayMessage, 0, limit)}
	// Reserve envelope/cursor space; JSON escaping is measured, not estimated.
	remaining := conversation.MaxMessagePageBytes - 256
	var oldest string
	more := false
	for rows.Next() {
		if len(page.Messages) == limit {
			more = true
			break
		}
		var turnSequence, messageSequence, startedAt int64
		var completedAt sql.NullInt64
		var text sql.NullString
		var item conversation.DisplayMessage
		if err := rows.Scan(&turnSequence, &item.TurnID, &messageSequence, &item.Role, &text, &startedAt, &completedAt); err != nil {
			return conversation.MessagePage{}, fmt.Errorf("scan display message: %w", err)
		}
		if turnSequence < 1 || messageSequence < 1 || !completedAt.Valid {
			return conversation.MessagePage{}, fmt.Errorf("invalid stored display message")
		}
		item.ID = fmt.Sprintf("%s:%d", item.TurnID, messageSequence)
		item.Text = text.String
		item.CreatedAt = fromUnixNano(startedAt)
		if item.Role == "assistant" {
			item.CreatedAt = fromUnixNano(completedAt.Int64)
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return conversation.MessagePage{}, fmt.Errorf("encode display message: %w", err)
		}
		if !text.Valid || len(encoded)+1 > remaining {
			if len(page.Messages) == 0 {
				return conversation.MessagePage{}, conversation.ErrMessageTooLarge
			}
			more = true
			break
		}
		page.Messages = append(page.Messages, item)
		remaining -= len(encoded) + 1
		oldest = fmt.Sprintf("%d:%d", turnSequence, messageSequence)
	}
	if err := rows.Err(); err != nil {
		return conversation.MessagePage{}, fmt.Errorf("iterate display messages: %w", err)
	}
	if err := rows.Close(); err != nil {
		return conversation.MessagePage{}, fmt.Errorf("close display messages: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return conversation.MessagePage{}, fmt.Errorf("commit list messages transaction: %w", err)
	}
	if more {
		page.NextCursor = oldest
	}
	slices.Reverse(page.Messages)
	return page, nil
}
