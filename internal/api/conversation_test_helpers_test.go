package api

import (
	"context"

	"github.com/opensvc/ai-agent/internal/agent"
	"github.com/opensvc/ai-agent/internal/auth"
	"github.com/opensvc/ai-agent/internal/conversation"
	"github.com/opensvc/ai-agent/internal/llm"
)

type noopConversationService struct{}

func (noopConversationService) Messages(context.Context, auth.Identity, string, conversation.MessageQuery) (conversation.MessagePage, error) {
	return conversation.MessagePage{}, conversation.ErrNotFound
}

func (noopConversationService) Create(context.Context, auth.Identity) (conversation.Conversation, error) {
	return conversation.Conversation{}, nil
}

func (noopConversationService) Get(context.Context, auth.Identity, string) (conversation.Conversation, error) {
	return conversation.Conversation{}, conversation.ErrNotFound
}

func (noopConversationService) List(context.Context, auth.Identity) ([]conversation.Conversation, error) {
	return nil, nil
}

func (noopConversationService) Delete(context.Context, auth.Identity, string) error {
	return conversation.ErrNotFound
}

func (noopConversationService) UpdateTitle(context.Context, auth.Identity, string, string) (conversation.Conversation, error) {
	return conversation.Conversation{}, conversation.ErrNotFound
}

func (noopConversationService) PrepareTurn(context.Context, auth.Identity, string, string) (conversation.TurnExecution, error) {
	return nil, conversation.ErrNotFound
}

func (noopConversationService) PrepareConfirmation(context.Context, auth.Identity, string, string, string, agent.Decision) (conversation.TurnExecution, error) {
	return nil, conversation.ErrNotFound
}

func (noopConversationService) PendingConfirmation(context.Context, auth.Identity, string) (*conversation.PendingConfirmation, error) {
	return nil, conversation.ErrNotFound
}

func (noopConversationService) PendingClientCalls(context.Context, auth.Identity, string) ([]string, error) {
	return nil, nil
}

func (noopConversationService) AbandonClientCalls(context.Context, auth.Identity, string) error {
	return nil
}

func (noopConversationService) PrepareClientResults(context.Context, auth.Identity, string, []llm.ToolResult) (conversation.TurnExecution, error) {
	return nil, conversation.ErrNotPending
}

// testConversationID is the conversation whose turns turnService runs.
const testConversationID = "conversation-1"

// turnFunc runs one conversation turn in tests of the turn stream.
type turnFunc func(context.Context, string, agent.EmitFunc) error

// turnService runs every turn of testConversationID with run.
type turnService struct {
	noopConversationService
	run turnFunc
}

func (s turnService) PrepareTurn(_ context.Context, _ auth.Identity, id string, prompt string) (conversation.TurnExecution, error) {
	if id != testConversationID {
		return nil, conversation.ErrNotFound
	}
	return &testTurnExecution{run: func(ctx context.Context, emit agent.EmitFunc) error {
		return s.run(ctx, prompt, emit)
	}}, nil
}

func (noopConversationService) SetFeedback(context.Context, auth.Identity, string, string, conversation.Feedback) (conversation.FeedbackRecord, error) {
	return conversation.FeedbackRecord{}, nil
}

func (noopConversationService) DeleteFeedback(context.Context, auth.Identity, string, string) error {
	return nil
}
