//go:build agui_conformance

package api

// The conformance test runs the official AG-UI client, @ag-ui/client at the
// version testdata/agui/package.json pins, against the runs route. It needs
// Node and the client installed:
//
//	(cd internal/api/testdata/agui && npm ci --ignore-scripts)
//	go test -tags agui_conformance -run TestAGUIClientConformance ./internal/api/
//
// Run it again on each new version of the client.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/opensvc/ai-agent/internal/agent"
	"github.com/opensvc/ai-agent/internal/auth"
	"github.com/opensvc/ai-agent/internal/conversation"
	"github.com/opensvc/ai-agent/internal/llm"
)

func emitAll(emit agent.EmitFunc, events []agent.Event) error {
	for _, event := range events {
		if err := emit(event); err != nil {
			return err
		}
	}
	return nil
}

func TestAGUIClientConformance(t *testing.T) {
	state := `{"messages":[],"iteration":2,"calls":[{"ID":"call-7","Name":"stop_object","Arguments":{"path":"web"}}],"results":[]}`
	service := conversationServiceFuncs{
		prepare: func(_ context.Context, _ auth.Identity, _ string, prompt string) (conversation.TurnExecution, error) {
			if prompt == "status" {
				// The conversation stored a turn: a client that lacks it
				// receives the thread.
				return &testTurnExecution{turnID: "turn-2", history: []llm.Message{
					{Role: llm.RoleUser, Text: "nodes?"},
					{Role: llm.RoleAssistant, Text: "Looking.", ToolCalls: []llm.ToolCall{{ID: "call-h1", Name: "list_nodes", Arguments: json.RawMessage(`{}`)}}},
					{Role: llm.RoleTool, ToolResults: []llm.ToolResult{{CallID: "call-h1", Content: json.RawMessage(`{"nodes":["n1","n2"]}`)}}},
					{Role: llm.RoleAssistant, Text: "Two nodes."},
				}, historyIDs: []string{"u-nodes", "turn-1:2", "turn-1:3", "turn-1:4"}, run: func(_ context.Context, emit agent.EmitFunc) error {
					return emitAll(emit, []agent.Event{
						{Type: agent.EventTextDelta, TextDelta: "All up.", Iteration: 1},
						{Type: agent.EventCompleted, FinishReason: llm.FinishReasonCompleted, TurnID: "turn-2", Iteration: 1},
					})
				}}, nil
			}
			if prompt == "show web" {
				// The model calls a tool the client offers: the run leaves
				// the call to the client.
				return &testTurnExecution{run: func(ctx context.Context, emit agent.EmitFunc) error {
					if tools := agent.ClientToolsForTest(ctx); len(tools) != 1 || tools[0].Name != "open_page" {
						return errors.New("the tools of the client did not reach the turn")
					}
					return emitAll(emit, []agent.Event{
						{Type: agent.EventTextDelta, TextDelta: "Opening web.", Iteration: 1},
						{Type: agent.EventToolStarted, ToolName: "open_page", ToolCallID: "ui-1", ToolArguments: json.RawMessage(`{"path":"/web"}`), Iteration: 1},
						{Type: agent.EventClientToolsPending, PendingCalls: []string{"ui-1"}, Iteration: 1},
					})
				}}, nil
			}
			return &testTurnExecution{run: func(_ context.Context, emit agent.EmitFunc) error {
				return emitAll(emit, []agent.Event{
					{Type: agent.EventTextDelta, TextDelta: "Let me check.", Iteration: 1},
					{Type: agent.EventToolStarted, ToolName: "get_object", ToolCallID: "call-1", ToolArguments: json.RawMessage(`{"path":"web"}`), Iteration: 1},
					{Type: agent.EventToolFinished, ToolName: "get_object", ToolCallID: "call-1", ToolResult: json.RawMessage(`{"avail":"up"}`), Iteration: 1},
					{Type: agent.EventUsage, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}, Iteration: 1},
					{Type: agent.EventTextDelta, TextDelta: "Stopping web.", Iteration: 2},
					{Type: agent.EventConfirmationRequired, Iteration: 2, Confirmation: &agent.Confirmation{ID: "confirmation-id", TurnID: "turn-id", ToolName: "stop_object",
						ToolTitle: "Stop object", Destructive: true, ToolCallID: "call-7", Arguments: json.RawMessage(`{"path":"web"}`), ExpiresAt: time.Now().Add(10 * time.Minute)}},
				})
			}}, nil
		},
		pending: func(context.Context, auth.Identity, string) (*conversation.PendingConfirmation, error) {
			return &conversation.PendingConfirmation{TurnID: "turn-id", ConfirmationID: "confirmation-id", State: []byte(state)}, nil
		},
		confirm: func(context.Context, auth.Identity, string, string, string, agent.Decision) (conversation.TurnExecution, error) {
			return &testTurnExecution{run: func(_ context.Context, emit agent.EmitFunc) error {
				return emitAll(emit, []agent.Event{
					{Type: agent.EventToolStarted, ToolName: "stop_object", ToolCallID: "call-7", ToolArguments: json.RawMessage(`{"path":"web"}`), Iteration: 2},
					{Type: agent.EventToolFinished, ToolName: "stop_object", ToolCallID: "call-7", ToolResult: json.RawMessage(`{"ok":true}`), Iteration: 2},
					{Type: agent.EventTextDelta, TextDelta: "web is stopped.", Iteration: 3},
					{Type: agent.EventCompleted, FinishReason: llm.FinishReasonCompleted, TurnID: "turn-id", Iteration: 3},
				})
			}}, nil
		},
	}
	service.clientCalls = func(context.Context, auth.Identity, string) ([]string, error) { return []string{"ui-1"}, nil }
	service.answer = func(_ context.Context, _ auth.Identity, _ string, results []llm.ToolResult) (conversation.TurnExecution, error) {
		if len(results) != 1 || results[0].CallID != "ui-1" {
			return nil, conversation.ErrInvalidToolResults
		}
		return &testTurnExecution{run: func(_ context.Context, emit agent.EmitFunc) error {
			return emitAll(emit, []agent.Event{
				{Type: agent.EventTextDelta, TextDelta: "web is shown.", Iteration: 2},
				{Type: agent.EventCompleted, FinishReason: llm.FinishReasonCompleted, TurnID: "turn-ui", Iteration: 2},
			})
		}}, nil
	}
	server := httptest.NewServer(newConversationTestHandler(t, service))
	defer server.Close()
	command := exec.Command("node", "conformance.mjs", server.URL)
	command.Dir = "testdata/agui"
	command.Env = append(command.Environ(), "SUPPRESS_TRANSFORMATION_WARNINGS=1")
	output, err := command.CombinedOutput()
	t.Logf("%s", output)
	if err != nil {
		t.Fatal(err)
	}
}
