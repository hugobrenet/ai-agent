package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/opensvc/ai-agent/internal/agent"
	"github.com/opensvc/ai-agent/internal/llm"
)

// previewRunner computes the preview of the call waiting again, with the
// edited arguments, or fails to.
type previewRunner struct {
	editableRunner
	fail bool
}

func (r *previewRunner) ResumeTurn(ctx context.Context, history []llm.Message, state agent.SuspendedTurn, decision agent.Decision, emit agent.EmitFunc) (agent.TurnResult, error) {
	if decision != agent.DecisionPreview {
		return r.editableRunner.ResumeTurn(ctx, history, state, decision, emit)
	}
	if r.fail {
		return agent.TurnResult{}, errors.New("connect agent to MCP: refused")
	}
	state.Preview, state.PreviewArguments, state.EditedArguments = json.RawMessage(`{"summary":{"key":"stop_object"}}`), state.EditedArguments, nil
	return agent.TurnResult{Suspended: &state}, nil
}

func suspendPreviewed(t *testing.T, runner *previewRunner) (*Service, *serviceTestStore, *agent.Confirmation) {
	t.Helper()
	store := newServiceTestStore()
	service := newTestService(t, store, runner)
	execution, err := service.PrepareTurn(t.Context(), serviceTestIdentity(), store.item.ID, "stop web")
	if err != nil {
		t.Fatal(err)
	}
	var request *agent.Confirmation
	if err := execution.Run(t.Context(), func(event agent.Event) error { request = event.Confirmation; return nil }); err != nil {
		t.Fatal(err)
	}
	return service, store, request
}

func TestServiceAsksThePreviewAgainWithoutDeciding(t *testing.T) {
	runner := &previewRunner{}
	service, store, request := suspendPreviewed(t, runner)
	execution, err := service.PrepareConfirmation(t.Context(), serviceTestIdentity(), store.item.ID, request.TurnID, request.ID, agent.DecisionPreview, json.RawMessage(`{"path":"db"}`))
	if err != nil {
		t.Fatal(err)
	}
	var again *agent.Confirmation
	if err := execution.Run(t.Context(), func(event agent.Event) error { again = event.Confirmation; return nil }); err != nil {
		t.Fatal(err)
	}
	// The call waits again, under a new confirmation, with the preview of the edit.
	// The test service gives every ID the same value: the store holds the new one.
	if again == nil || again.ToolCallID != request.ToolCallID || string(again.Arguments) != string(request.Arguments) ||
		string(again.PreviewArguments) != `{"path":"db"}` || string(again.Preview) != `{"summary":{"key":"stop_object"}}` {
		t.Fatalf("confirmation %+v after %+v", again, request)
	}
	if store.suspended == nil || store.suspended.ConfirmationID != again.ID || store.completed || store.failed {
		t.Fatalf("store %+v", store)
	}
	// A preview asked again takes the edited arguments.
	if _, err := service.PrepareConfirmation(t.Context(), serviceTestIdentity(), store.item.ID, again.TurnID, again.ID, agent.DecisionPreview, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a preview without arguments: %v", err)
	}
}

func TestServiceKeepsTheCallWaitingWhenThePreviewFailsAgain(t *testing.T) {
	runner := &previewRunner{fail: true}
	service, store, request := suspendPreviewed(t, runner)
	execution, err := service.PrepareConfirmation(t.Context(), serviceTestIdentity(), store.item.ID, request.TurnID, request.ID, agent.DecisionPreview, json.RawMessage(`{"path":"db"}`))
	if err != nil {
		t.Fatal(err)
	}
	var again *agent.Confirmation
	if err := execution.Run(t.Context(), func(event agent.Event) error { again = event.Confirmation; return nil }); err != nil {
		t.Fatal(err)
	}
	if again == nil || again.PreviewError == "" || again.PreviewArguments != nil || store.failed || store.suspended == nil {
		t.Fatalf("confirmation %+v, store failed %v", again, store.failed)
	}
}
