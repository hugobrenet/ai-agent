package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/opensvc/ai-agent/internal/agent"
	"github.com/opensvc/ai-agent/internal/llm"
)

// clientToolsRunner suspends the first turn on two calls of tools of the
// client, then records the answers it is resumed with.
type clientToolsRunner struct {
	resumedWith []llm.ToolResult
	decision    agent.Decision
}

func (r *clientToolsRunner) RunTurn(context.Context, []llm.Message, string, agent.EmitFunc) (agent.TurnResult, error) {
	state := agent.SuspendedTurn{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "show web"}}, Iteration: 1, TotalToolCalls: 2,
		Calls: []llm.ToolCall{
			{ID: "ui-1", Name: "open_page", Arguments: json.RawMessage(`{"path":"/web"}`)},
			{ID: "ui-2", Name: "highlight", Arguments: json.RawMessage(`{"row":1}`)},
		},
		ClientCalls: []string{"ui-1", "ui-2"},
	}
	return agent.TurnResult{Suspended: &state}, nil
}

func (r *clientToolsRunner) ResumeTurn(_ context.Context, _ []llm.Message, state agent.SuspendedTurn, decision agent.Decision, emit agent.EmitFunc) (agent.TurnResult, error) {
	r.resumedWith, r.decision = state.ClientResults, decision
	if err := emit(agent.Event{Type: agent.EventCompleted, FinishReason: llm.FinishReasonCompleted, Iteration: 2}); err != nil {
		return agent.TurnResult{}, err
	}
	return agent.TurnResult{Messages: append(state.Messages, llm.Message{Role: llm.RoleAssistant, Text: "shown"}), FinishReason: llm.FinishReasonCompleted}, nil
}

func suspendOnClientTools(t *testing.T) (*Service, *serviceTestStore, *clientToolsRunner) {
	t.Helper()
	store := newServiceTestStore()
	runner := &clientToolsRunner{}
	service := newTestService(t, store, runner)
	execution, err := service.PrepareTurn(t.Context(), serviceTestIdentity(), store.item.ID, "show web")
	if err != nil {
		t.Fatal(err)
	}
	var pending []string
	if err := execution.Run(t.Context(), func(event agent.Event) error {
		if event.Type != agent.EventClientToolsPending || store.suspended == nil {
			t.Fatalf("unexpected event %+v, stored %v", event, store.suspended != nil)
		}
		pending = event.PendingCalls
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pending, []string{"ui-1", "ui-2"}) {
		t.Fatalf("pending = %v", pending)
	}
	return service, store, runner
}

func TestServiceResumesATurnWithTheAnswersOfTheClient(t *testing.T) {
	service, store, runner := suspendOnClientTools(t)
	identity := serviceTestIdentity()
	// The wait is no confirmation: it is not shown as one, nor decided.
	if pending, err := service.PendingConfirmation(t.Context(), identity, store.item.ID); err != nil || pending != nil {
		t.Fatalf("pending confirmation = %+v, %v", pending, err)
	}
	if _, err := service.PrepareConfirmation(t.Context(), identity, store.item.ID, store.suspended.TurnID, store.suspended.ConfirmationID, agent.DecisionApprove); !errors.Is(err, ErrNotPending) {
		t.Fatalf("decision on a client wait: %v", err)
	}
	if calls, err := service.PendingClientCalls(t.Context(), identity, store.item.ID); err != nil || !slices.Equal(calls, []string{"ui-1", "ui-2"}) {
		t.Fatalf("pending client calls = %v, %v", calls, err)
	}
	// Answers must be exactly one per call waiting.
	for _, results := range [][]llm.ToolResult{
		{agent.ClientToolResult("ui-1", "opened", false)},
		{agent.ClientToolResult("ui-1", "opened", false), agent.ClientToolResult("ui-1", "again", false)},
		{agent.ClientToolResult("ui-1", "opened", false), agent.ClientToolResult("other", "x", false)},
	} {
		if _, err := service.PrepareClientResults(t.Context(), identity, store.item.ID, results); !errors.Is(err, ErrInvalidToolResults) || store.suspended == nil {
			t.Fatalf("answers %v: %v", results, err)
		}
	}
	answers := []llm.ToolResult{agent.ClientToolResult("ui-2", "highlighted", false), agent.ClientToolResult("ui-1", "failed", true)}
	execution, err := service.PrepareClientResults(t.Context(), identity, store.item.ID, answers)
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.Run(t.Context(), func(agent.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if runner.decision != "" || len(runner.resumedWith) != 2 || !store.completed || store.suspended != nil {
		t.Fatalf("resumed with %v %q, completed %v", runner.resumedWith, runner.decision, store.completed)
	}
}

func TestServiceAbandonsTheCallsOfTheClient(t *testing.T) {
	service, store, _ := suspendOnClientTools(t)
	if err := service.AbandonClientCalls(t.Context(), serviceTestIdentity(), store.item.ID); err != nil {
		t.Fatal(err)
	}
	if store.suspended != nil || !store.failed || store.failureCode != "client_tools_abandoned" {
		t.Fatalf("abandon: suspended %v, failed %v %q", store.suspended != nil, store.failed, store.failureCode)
	}
	// A confirmation is not abandoned.
	confirmations := newServiceTestStore()
	confirming := newTestService(t, confirmations, &confirmationRunner{})
	execution, err := confirming.PrepareTurn(t.Context(), serviceTestIdentity(), confirmations.item.ID, "stop web")
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.Run(t.Context(), func(agent.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := confirming.AbandonClientCalls(t.Context(), serviceTestIdentity(), confirmations.item.ID); err != nil || confirmations.suspended == nil {
		t.Fatalf("abandon of a confirmation: %v, suspended %v", err, confirmations.suspended != nil)
	}
}
