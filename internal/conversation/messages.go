package conversation

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/opensvc/ai-agent/internal/auth"
)

const (
	DefaultMessagePageLimit = 50
	MaxMessagePageLimit     = 100
	MaxMessagePageBytes     = 1 << 20
)

// DisplayMessage is a public transcript projection, not model context. Tool
// arguments/results, system prompts and provider state must never be added here.
type DisplayMessage struct {
	ID        string    `json:"id"`
	TurnID    string    `json:"turn_id"`
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

type MessagePage struct {
	Messages   []DisplayMessage `json:"messages"`
	NextCursor string           `json:"next_cursor"`
}

type MessageQuery struct {
	Limit  int
	Before string
}

// Bounds supplies defaults and validates a strict, exclusive sequence cursor.
// A cursor is only a position within an owned conversation, never authority.
func (q MessageQuery) Bounds() (int, int64, int64, error) {
	limit := q.Limit
	if limit == 0 {
		limit = DefaultMessagePageLimit
	}
	if limit < 1 || limit > MaxMessagePageLimit {
		return 0, 0, 0, fmt.Errorf("%w: invalid message page limit", ErrInvalid)
	}
	if q.Before == "" {
		return limit, math.MaxInt64, math.MaxInt64, nil
	}
	if len(q.Before) > 39 {
		return 0, 0, 0, fmt.Errorf("%w: invalid message cursor", ErrInvalid)
	}
	turnText, messageText, ok := strings.Cut(q.Before, ":")
	turn, turnErr := strconv.ParseInt(turnText, 10, 64)
	message, messageErr := strconv.ParseInt(messageText, 10, 64)
	if !ok || turnErr != nil || messageErr != nil || turn < 1 || message < 1 ||
		q.Before != fmt.Sprintf("%d:%d", turn, message) {
		return 0, 0, 0, fmt.Errorf("%w: invalid message cursor", ErrInvalid)
	}
	return limit, turn, message, nil
}

func (s *Service) Messages(ctx context.Context, identity auth.Identity, id string, query MessageQuery) (MessagePage, error) {
	limit, _, _, err := query.Bounds()
	if err != nil {
		return MessagePage{}, err
	}
	item, err := s.Get(ctx, identity, id)
	if err != nil {
		return MessagePage{}, err
	}
	query.Limit = limit
	return s.store.ListMessages(ctx, item.Owner, item.ID, query)
}
