package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/opensvc/ai-agent/internal/auth"
	"github.com/opensvc/ai-agent/internal/llm"
)

const (
	FeedbackUp   = "up"
	FeedbackDown = "down"

	// MaxFeedbackExpectedRunes bounds what the user would have expected.
	MaxFeedbackExpectedRunes = 1000
	// DefaultFeedbackRetention is how long a feedback is kept, whatever
	// becomes of its conversation.
	DefaultFeedbackRetention = 180 * 24 * time.Hour

	maxSnapshotTextBytes      = 4 << 10
	maxSnapshotArgumentsBytes = 1 << 10
	maxSnapshotResultBytes    = 512
	maxSnapshotToolCalls      = 16
)

// FeedbackReasons are the reasons a user may give with a negative feedback.
var FeedbackReasons = []string{
	"not_answered",
	"missing_information",
	"incorrect_or_outdated",
	"hard_to_understand",
	"action_not_done",
}

// Feedback is the opinion of the user on one turn: a rating, the reasons of
// a negative one, and what the user would have expected.
type Feedback struct {
	Rating   string   `json:"rating"`
	Reasons  []string `json:"reasons"`
	Expected string   `json:"expected"`
}

// Validate checks a feedback given by the user and returns it with its
// reasons in a stable order.
func (f Feedback) Validate() (Feedback, error) {
	if f.Rating != FeedbackUp && f.Rating != FeedbackDown {
		return Feedback{}, fmt.Errorf("%w: feedback rating must be up or down", ErrInvalid)
	}
	if f.Rating == FeedbackUp && len(f.Reasons) != 0 {
		return Feedback{}, fmt.Errorf("%w: feedback reasons require a down rating", ErrInvalid)
	}
	reasons := make([]string, 0, len(f.Reasons))
	for _, reason := range f.Reasons {
		if !slices.Contains(FeedbackReasons, reason) || slices.Contains(reasons, reason) {
			return Feedback{}, fmt.Errorf("%w: feedback reason is unknown or repeated", ErrInvalid)
		}
		reasons = append(reasons, reason)
	}
	slices.SortFunc(reasons, func(a, b string) int { return slices.Index(FeedbackReasons, a) - slices.Index(FeedbackReasons, b) })
	f.Reasons = reasons
	f.Expected = strings.TrimSpace(f.Expected)
	if !utf8.ValidString(f.Expected) || utf8.RuneCountInString(f.Expected) > MaxFeedbackExpectedRunes ||
		strings.ContainsFunc(f.Expected, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) {
		return Feedback{}, fmt.Errorf("%w: feedback expected text must be at most %d characters of printable text", ErrInvalid, MaxFeedbackExpectedRunes)
	}
	return f, nil
}

// FeedbackRecord is a stored feedback. It carries no identity: TurnID links
// it to its turn only while the conversation exists, for the user to change
// or withdraw it.
type FeedbackRecord struct {
	ID        string
	TurnID    string
	Feedback  Feedback
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt time.Time
	// Snapshot is the bounded copy of the turn taken with the first
	// feedback: question, answer and tool calls.
	Snapshot json.RawMessage
	// Context describes the agent that answered: backend, model and system
	// prompt, as configured when the feedback was given.
	Context json.RawMessage
}

// MaxFeedbackContextBytes bounds the context of a feedback, so that a
// feedback is always stored beside its bounded snapshot.
const MaxFeedbackContextBytes = 16 << 10

// FeedbackContext describes what answered a turn, in its feedback: the
// backend, and the runs of the turn as recorded while it executed. Truncated
// tells that, to fit MaxFeedbackContextBytes, shown tools were left out of
// runs, counted by their ShownToolsOmitted, and RunsOmitted runs between the
// first and the last ones.
type FeedbackContext struct {
	Backend     string    `json:"backend"`
	Runs        []TurnRun `json:"runs"`
	RunsOmitted int       `json:"runs_omitted,omitempty"`
	Truncated   bool      `json:"truncated,omitempty"`
}

// newFeedbackContext encodes the context of a feedback within
// MaxFeedbackContextBytes: it leaves out the shown tools of the runs, the
// oldest runs first, then the runs between the first and the last ones.
func newFeedbackContext(backend string, runs []TurnRun) (json.RawMessage, error) {
	context := FeedbackContext{Backend: backend, Runs: make([]TurnRun, len(runs))}
	for i, run := range runs {
		run.ShownTools = slices.Clone(run.ShownTools)
		if run.ShownTools == nil {
			run.ShownTools = []string{}
		}
		context.Runs[i] = run
	}
	for {
		data, err := json.Marshal(context)
		if err != nil {
			return nil, fmt.Errorf("encode feedback context: %w", err)
		}
		if len(data) <= MaxFeedbackContextBytes {
			return data, nil
		}
		context.Truncated = true
		if index := slices.IndexFunc(context.Runs, func(run TurnRun) bool { return len(run.ShownTools) > 0 }); index >= 0 {
			context.Runs[index].ShownToolsOmitted += len(context.Runs[index].ShownTools)
			context.Runs[index].ShownTools = []string{}
			continue
		}
		if len(context.Runs) > 2 {
			context.Runs = slices.Delete(context.Runs, 1, 2)
			context.RunsOmitted++
			continue
		}
		return nil, fmt.Errorf("feedback context exceeds %d bytes", MaxFeedbackContextBytes)
	}
}

// TurnSnapshot is the copy of a turn kept with its feedback.
type TurnSnapshot struct {
	Question  string             `json:"question"`
	Answer    string             `json:"answer"`
	ToolCalls []SnapshotToolCall `json:"tool_calls"`
	// ToolCallsOmitted counts the tool calls left out after the first ones.
	ToolCallsOmitted int       `json:"tool_calls_omitted,omitempty"`
	StartedAt        time.Time `json:"started_at"`
	CompletedAt      time.Time `json:"completed_at"`
	Truncated        bool      `json:"truncated"`
}

// SnapshotToolCall is one tool call of a turn: its arguments and the start of
// its result, each truncated.
type SnapshotToolCall struct {
	Name        string `json:"name"`
	Arguments   string `json:"arguments"`
	IsError     bool   `json:"is_error"`
	Result      string `json:"result,omitempty"`
	ResultBytes int    `json:"result_bytes"`
}

// NewTurnSnapshot copies the messages of a completed turn, bounded.
func NewTurnSnapshot(turn Turn, messages []llm.Message) TurnSnapshot {
	snapshot := TurnSnapshot{ToolCalls: []SnapshotToolCall{}, StartedAt: turn.StartedAt}
	if turn.CompletedAt != nil {
		snapshot.CompletedAt = *turn.CompletedAt
	}
	cut := func(text string, limit int) string {
		if len(text) <= limit {
			return text
		}
		snapshot.Truncated = true
		for limit > 0 && !utf8.RuneStart(text[limit]) {
			limit--
		}
		return text[:limit] + "…"
	}
	calls := map[string]int{}
	var answers []string
	for _, message := range messages {
		switch message.Role {
		case llm.RoleUser:
			if snapshot.Question == "" {
				snapshot.Question = cut(message.Text, maxSnapshotTextBytes)
			}
		case llm.RoleAssistant:
			if message.Text != "" {
				answers = append(answers, message.Text)
			}
			for _, call := range message.ToolCalls {
				if len(snapshot.ToolCalls) == maxSnapshotToolCalls {
					snapshot.ToolCallsOmitted++
					snapshot.Truncated = true
					continue
				}
				calls[call.ID] = len(snapshot.ToolCalls)
				snapshot.ToolCalls = append(snapshot.ToolCalls, SnapshotToolCall{Name: call.Name, Arguments: cut(string(call.Arguments), maxSnapshotArgumentsBytes)})
			}
		case llm.RoleTool:
			for _, result := range message.ToolResults {
				index, ok := calls[result.CallID]
				if !ok {
					continue
				}
				snapshot.ToolCalls[index].IsError = result.IsError
				snapshot.ToolCalls[index].ResultBytes = len(result.Content)
				snapshot.ToolCalls[index].Result = cut(string(result.Content), maxSnapshotResultBytes)
			}
		}
	}
	snapshot.Answer = cut(strings.Join(answers, "\n\n"), maxSnapshotTextBytes)
	return snapshot
}

// SetFeedback records the feedback of the user on a completed turn of their
// conversation. The first feedback of a turn copies it; a new one replaces
// the rating, the reasons and the expected text, and keeps the copy.
func (s *Service) SetFeedback(ctx context.Context, identity auth.Identity, conversationID string, turnID string, feedback Feedback) (FeedbackRecord, error) {
	feedback, err := feedback.Validate()
	if err != nil {
		return FeedbackRecord{}, err
	}
	item, err := s.Get(ctx, identity, conversationID)
	if err != nil {
		return FeedbackRecord{}, err
	}
	turn, messages, err := s.store.TurnMessages(ctx, item.Owner, item.ID, turnID)
	if err != nil {
		return FeedbackRecord{}, err
	}
	if turn.Status != TurnCompleted {
		return FeedbackRecord{}, fmt.Errorf("%w: only a completed turn takes a feedback", ErrInvalid)
	}
	snapshot, err := json.Marshal(NewTurnSnapshot(turn, messages))
	if err != nil {
		return FeedbackRecord{}, fmt.Errorf("encode turn snapshot: %w", err)
	}
	agentContext, err := newFeedbackContext(s.config.Backend, turn.Runs)
	if err != nil {
		return FeedbackRecord{}, err
	}
	id, err := s.newID()
	if err != nil {
		return FeedbackRecord{}, fmt.Errorf("generate feedback ID: %w", err)
	}
	now := s.now().UTC()
	return s.store.PutFeedback(ctx, item.Owner, item.ID, FeedbackRecord{
		ID: id, TurnID: turnID, Feedback: feedback, CreatedAt: now, UpdatedAt: now,
		ExpiresAt: now.Add(s.config.FeedbackRetention), Snapshot: snapshot, Context: agentContext,
	})
}

// DeleteFeedback withdraws the feedback of the user on a turn of their
// conversation.
func (s *Service) DeleteFeedback(ctx context.Context, identity auth.Identity, conversationID string, turnID string) error {
	item, err := s.Get(ctx, identity, conversationID)
	if err != nil {
		return err
	}
	return s.store.DeleteFeedback(ctx, item.Owner, item.ID, turnID)
}
