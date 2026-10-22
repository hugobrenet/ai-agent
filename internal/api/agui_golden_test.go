package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/opensvc/ai-agent/internal/agent"
	"github.com/opensvc/ai-agent/internal/auth"
	"github.com/opensvc/ai-agent/internal/conversation"
	"github.com/opensvc/ai-agent/internal/llm"
)

// goldenStreamsPath holds the AG-UI streams the runs route serves in the
// scenarios of a client: the web interfaces replay them in their tests, so
// that a change of the stream breaks them. AGUI_GOLDEN_UPDATE=1 rewrites it.
var goldenStreamsPath = filepath.Join("testdata", "agui", "streams.json")

// goldenStream is one scenario: the RunAgentInput, and the answer of the
// route, an SSE stream or a JSON error.
type goldenStream struct {
	Name        string          `json:"name"`
	Request     json.RawMessage `json:"request"`
	Status      int             `json:"status"`
	ContentType string          `json:"contentType"`
	Body        string          `json:"body"`
}

// goldenTimestamp replaces the time of each event, for the file to be stable.
var goldenTimestamp = regexp.MustCompile(`"timestamp":\d+`)

func emitEvents(events ...agent.Event) func(context.Context, agent.EmitFunc) error {
	return func(_ context.Context, emit agent.EmitFunc) error {
		for _, event := range events {
			if err := emit(event); err != nil {
				return err
			}
		}
		return nil
	}
}

func usageEvent(input, output int64) agent.Event {
	return agent.Event{Type: agent.EventUsage, Usage: &llm.Usage{InputTokens: input, OutputTokens: output, TotalTokens: input + output}}
}

func TestGoldenAGUIStreams(t *testing.T) {
	confirmation := &agent.Confirmation{
		ID: "confirmation-1", TurnID: "turn-c", ToolName: "update_object_config", ToolTitle: "Update object config", Destructive: true,
		ToolCallID: "call-7", Arguments: json.RawMessage(`{"path":"web","yaml":"app#1:\n  start: /bin/true\n"}`),
		ArgumentTypes: map[string]string{"yaml": "application/yaml"}, ExpiresAt: confirmationExpiry,
	}
	pending := func(context.Context, auth.Identity, string) (*conversation.PendingConfirmation, error) {
		state := `{"messages":[],"iteration":1,"calls":[{"ID":"call-7","Name":"update_object_config","Arguments":{}}],"results":[]}`
		return &conversation.PendingConfirmation{TurnID: "turn-c", ConfirmationID: "confirmation-1", ToolName: "update_object_config", State: []byte(state)}, nil
	}
	prompt := func(execution *testTurnExecution) conversationServiceFuncs {
		return conversationServiceFuncs{pending: pending, prepare: func(context.Context, auth.Identity, string, string) (conversation.TurnExecution, error) {
			return execution, nil
		}}
	}
	resume := func(execution *testTurnExecution) conversationServiceFuncs {
		return conversationServiceFuncs{pending: pending, confirm: func(context.Context, auth.Identity, string, string, string, agent.Decision) (conversation.TurnExecution, error) {
			return execution, nil
		}}
	}
	scenarios := []struct {
		name    string
		service conversationServiceFuncs
		request string
	}{
		{
			// Two answers of the model in one turn, around the tools it calls:
			// search_tools is a tool of the agent itself.
			name: "text_and_tools",
			service: prompt(&testTurnExecution{turnID: "turn-1", run: emitEvents(
				agent.Event{Type: agent.EventTextDelta, TextDelta: "Let me check ", Iteration: 1},
				agent.Event{Type: agent.EventTextDelta, TextDelta: "the nodes.", Iteration: 1},
				agent.Event{Type: agent.EventToolStarted, ToolName: "search_tools", ToolCallID: "call-0", ToolArguments: json.RawMessage(`{"query":"nodes"}`), Iteration: 1},
				agent.Event{Type: agent.EventToolFinished, ToolName: "search_tools", ToolCallID: "call-0", ToolResult: json.RawMessage(`{"content":[{"type":"text","text":"list_nodes"}]}`), Iteration: 1},
				agent.Event{Type: agent.EventToolStarted, ToolName: "list_nodes", ToolCallID: "call-1", ToolArguments: json.RawMessage(`{}`), Iteration: 1},
				agent.Event{Type: agent.EventToolFinished, ToolName: "list_nodes", ToolCallID: "call-1", ToolResult: json.RawMessage(`{"content":[],"structuredContent":{"nodes":["n1","n2"]}}`), Iteration: 1},
				usageEvent(120, 30),
				agent.Event{Type: agent.EventTextDelta, TextDelta: "There are two nodes: **n1** and **n2**.", Iteration: 2},
				usageEvent(200, 20),
				agent.Event{Type: agent.EventCompleted, FinishReason: llm.FinishReasonCompleted, TurnID: "turn-1", Iteration: 2},
			)}),
			request: `{"threadId":"conversation-id","runId":"run-1","protocolVersion":"1.0","messages":[{"id":"u-1","role":"user","content":"How many nodes?"}],"tools":[]}`,
		},
		{
			// The run ends on the call it asks the user to confirm.
			name: "confirmation",
			service: prompt(&testTurnExecution{turnID: "turn-c", run: emitEvents(
				agent.Event{Type: agent.EventTextDelta, TextDelta: "I will apply this configuration.", Iteration: 1},
				usageEvent(300, 40),
				agent.Event{Type: agent.EventConfirmationRequired, Iteration: 1, Confirmation: confirmation},
			)}),
			request: `{"threadId":"conversation-id","runId":"run-1","protocolVersion":"1.0","messages":[{"id":"u-c","role":"user","content":"Start web with /bin/true"}],"tools":[]}`,
		},
		{
			// The approved call runs: its result, then the answer.
			name: "resume_approved",
			service: resume(&testTurnExecution{turnID: "turn-c", run: emitEvents(
				agent.Event{Type: agent.EventToolStarted, ToolName: "update_object_config", ToolCallID: "call-7", ToolArguments: confirmation.Arguments, Iteration: 1},
				agent.Event{Type: agent.EventToolFinished, ToolName: "update_object_config", ToolCallID: "call-7", ToolResult: json.RawMessage(`{"content":[{"type":"text","text":"updated"}]}`), Iteration: 1},
				agent.Event{Type: agent.EventTextDelta, TextDelta: "Done: web starts with /bin/true.", Iteration: 2},
				usageEvent(400, 15),
				agent.Event{Type: agent.EventCompleted, FinishReason: llm.FinishReasonCompleted, TurnID: "turn-c", Iteration: 2},
			)}),
			request: `{"threadId":"conversation-id","runId":"run-1","protocolVersion":"1.0","messages":[{"id":"u-c","role":"user"},{"id":"turn-c:2","role":"assistant"}],"tools":[],"resume":[{"interruptId":"confirmation-1","status":"resolved","payload":{"approved":true}}]}`,
		},
		{
			// The rejected call does not run: its result says so.
			name: "resume_rejected",
			service: resume(&testTurnExecution{turnID: "turn-c", run: emitEvents(
				agent.Event{Type: agent.EventTextDelta, TextDelta: "I did not apply it.", Iteration: 2},
				agent.Event{Type: agent.EventCompleted, FinishReason: llm.FinishReasonCompleted, TurnID: "turn-c", Iteration: 2},
			)}),
			request: `{"threadId":"conversation-id","runId":"run-1","protocolVersion":"1.0","messages":[{"id":"u-c","role":"user"},{"id":"turn-c:2","role":"assistant"}],"tools":[],"resume":[{"interruptId":"confirmation-1","status":"resolved","payload":{"approved":false}}]}`,
		},
		{
			name: "tool_call_limit",
			service: prompt(&testTurnExecution{turnID: "turn-l", run: func(_ context.Context, emit agent.EmitFunc) error {
				_ = emit(agent.Event{Type: agent.EventTextDelta, TextDelta: "Checking every node", Iteration: 1})
				return agent.ErrToolCallLimit
			}}),
			request: `{"threadId":"conversation-id","runId":"run-1","protocolVersion":"1.0","messages":[{"id":"u-l","role":"user","content":"Check everything"}],"tools":[]}`,
		},
		{
			name: "agent_failed",
			service: prompt(&testTurnExecution{turnID: "turn-f", run: func(context.Context, agent.EmitFunc) error {
				return errors.New("the model provider failed")
			}}),
			request: `{"threadId":"conversation-id","runId":"run-1","protocolVersion":"1.0","messages":[{"id":"u-f","role":"user","content":"Hello"}],"tools":[]}`,
		},
		{
			// A client that lacks the last stored turn, such as another tab,
			// receives the thread first, its prompt under the stored ID.
			name: "snapshot",
			service: prompt(&testTurnExecution{turnID: "turn-2",
				history: []llm.Message{
					{Role: llm.RoleUser, Text: "How many nodes?"},
					{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "list_nodes", Arguments: json.RawMessage(`{}`)}}},
					{Role: llm.RoleTool, ToolResults: []llm.ToolResult{{CallID: "call-1", Content: json.RawMessage(`{"content":[{"type":"text","text":"n1 n2"}]}`)}}},
					{Role: llm.RoleAssistant, Text: "Two nodes."},
				},
				historyIDs: []string{"u-1", "turn-1:2", "turn-1:3", "turn-1:4"},
				run: emitEvents(
					agent.Event{Type: agent.EventTextDelta, TextDelta: "No service.", Iteration: 1},
					agent.Event{Type: agent.EventCompleted, FinishReason: llm.FinishReasonCompleted, TurnID: "turn-2", Iteration: 1},
				)}),
			request: `{"threadId":"conversation-id","runId":"run-1","protocolVersion":"1.0","messages":[{"id":"u-2","role":"user","content":"Any service?"}],"tools":[]}`,
		},
		{
			// A client reloaded while the turn waits holds its earlier turns
			// only: the thread comes first, with the call waiting.
			name: "resume_snapshot",
			service: resume(&testTurnExecution{turnID: "turn-c",
				history:    []llm.Message{{Role: llm.RoleUser, Text: "How many nodes?"}, {Role: llm.RoleAssistant, Text: "Two nodes."}},
				historyIDs: []string{"u-1", "turn-1:2"},
				current: []llm.Message{
					{Role: llm.RoleUser, Text: "Start web with /bin/true"},
					{Role: llm.RoleAssistant, Text: "I will apply this configuration.", ToolCalls: []llm.ToolCall{{ID: "call-7", Name: "update_object_config", Arguments: confirmation.Arguments}}},
				},
				currentIDs: []string{"u-c", "turn-c:2"},
				run: emitEvents(
					agent.Event{Type: agent.EventTextDelta, TextDelta: "I did not apply it.", Iteration: 2},
					agent.Event{Type: agent.EventCompleted, FinishReason: llm.FinishReasonCompleted, TurnID: "turn-c", Iteration: 2},
				)}),
			request: `{"threadId":"conversation-id","runId":"run-1","protocolVersion":"1.0","messages":[{"id":"u-1","role":"user"},{"id":"turn-1:2","role":"assistant"}],"tools":[],"resume":[{"interruptId":"confirmation-1","status":"resolved","payload":{"approved":false}}]}`,
		},
		{
			name:    "confirmation_not_pending",
			service: resume(&testTurnExecution{run: emitEvents()}),
			request: `{"threadId":"conversation-id","runId":"run-1","protocolVersion":"1.0","messages":[],"tools":[],"resume":[{"interruptId":"confirmation-0","status":"resolved","payload":{"approved":true}}]}`,
		},
		{
			name: "conversation_busy",
			service: conversationServiceFuncs{prepare: func(context.Context, auth.Identity, string, string) (conversation.TurnExecution, error) {
				return nil, conversation.ErrBusy
			}},
			request: `{"threadId":"conversation-id","runId":"run-1","protocolVersion":"1.0","messages":[{"id":"u-b","role":"user","content":"Hello"}],"tools":[]}`,
		},
	}
	streams := make([]goldenStream, 0, len(scenarios))
	for _, scenario := range scenarios {
		response := runRequest(t, scenario.service, scenario.request)
		var request bytes.Buffer
		if err := json.Compact(&request, []byte(scenario.request)); err != nil {
			t.Fatalf("%s: %v", scenario.name, err)
		}
		streams = append(streams, goldenStream{
			Name: scenario.name, Request: request.Bytes(), Status: response.Code, ContentType: response.Header().Get("Content-Type"),
			Body: goldenTimestamp.ReplaceAllString(response.Body.String(), `"timestamp":1760000000000`),
		})
	}
	data, err := json.MarshalIndent(streams, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if os.Getenv("AGUI_GOLDEN_UPDATE") == "1" {
		if err := os.WriteFile(goldenStreamsPath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenStreamsPath)
	if err != nil {
		t.Fatalf("read %s, written with AGUI_GOLDEN_UPDATE=1: %v", goldenStreamsPath, err)
	}
	if !bytes.Equal(data, want) {
		t.Fatalf("the AG-UI streams differ from %s: rewrite it with AGUI_GOLDEN_UPDATE=1, and copy it to the web interfaces that replay it\n%s", goldenStreamsPath, data)
	}
}
