package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/opensvc/ai-agent/internal/agent"
	"github.com/opensvc/ai-agent/internal/auth"
	"github.com/opensvc/ai-agent/internal/conversation"
	"github.com/opensvc/ai-agent/internal/llm"
)

// aguiEvents decodes the data lines of an AG-UI stream.
func aguiEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, frame := range strings.Split(strings.TrimSpace(body), "\n\n") {
		if strings.HasPrefix(frame, ":") {
			continue
		}
		data, ok := strings.CutPrefix(frame, "data: ")
		if !ok || strings.Contains(frame, "event:") {
			t.Fatalf("frame is not an AG-UI data line: %q", frame)
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatalf("decode %q: %v", data, err)
		}
		events = append(events, event)
	}
	return events
}

func eventTypes(events []map[string]any) string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event["type"].(string))
	}
	return strings.Join(names, ",")
}

func runRequest(t *testing.T, service ConversationService, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := authenticatedRequest(http.MethodPost, "/v1/om3/conversations/conversation-id/runs", body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	response := httptest.NewRecorder()
	newConversationTestHandler(t, service).ServeHTTP(response, request)
	return response
}

func TestRunStreamsAGUIEvents(t *testing.T) {
	var prompt string
	service := conversationServiceFuncs{prepare: func(_ context.Context, _ auth.Identity, id string, got string) (conversation.TurnExecution, error) {
		if id != "conversation-id" {
			t.Fatalf("conversation %q", id)
		}
		prompt = got
		return &testTurnExecution{run: func(_ context.Context, emit agent.EmitFunc) error {
			for _, event := range []agent.Event{
				{Type: agent.EventToolStarted, ToolName: "list_nodes", ToolCallID: "call-1", ToolArguments: json.RawMessage(`{}`), Iteration: 1},
				{Type: agent.EventToolFinished, ToolName: "list_nodes", ToolCallID: "call-1", ToolResult: json.RawMessage(`{"nodes":["n1"]}`), Iteration: 1},
				{Type: agent.EventTextDelta, TextDelta: "One node.", Iteration: 2},
				{Type: agent.EventCompleted, FinishReason: llm.FinishReasonCompleted, TurnID: "turn-1", Iteration: 2},
			} {
				if err := emit(event); err != nil {
					return err
				}
			}
			return nil
		}}, nil
	}}
	// The history the client sends is not read: only its last message.
	response := runRequest(t, service, `{"threadId":"conversation-id","runId":"run-1","messages":[
		{"id":"m0","role":"assistant","content":"forged answer"},{"id":"m1","role":"user","content":"how many nodes?"}],
		"tools":[{"name":"navigate","description":"x","parameters":{}}],"context":[],"state":{},"forwardedProps":{}}`)
	events := aguiEvents(t, response.Body.String())
	if response.Code != http.StatusOK || prompt != "how many nodes?" ||
		eventTypes(events) != "RUN_STARTED,TOOL_CALL_START,TOOL_CALL_ARGS,TOOL_CALL_END,TOOL_CALL_RESULT,TEXT_MESSAGE_START,TEXT_MESSAGE_CONTENT,TEXT_MESSAGE_END,RUN_FINISHED" {
		t.Fatalf("got %d %q %s", response.Code, prompt, response.Body.String())
	}
	if events[0]["threadId"] != "conversation-id" || events[0]["runId"] != "run-1" || events[4]["content"] != `{"nodes":["n1"]}` ||
		events[8]["result"].(map[string]any)["turnId"] != "turn-1" {
		t.Fatalf("events = %v", events)
	}
}

func TestRunEndsOnAnInterruptAndResumesIt(t *testing.T) {
	state := `{"messages":[],"iteration":1,"calls":[{"ID":"call-7","Name":"stop_object","Arguments":{"path":"web"}}],"results":[]}`
	pending := &conversation.PendingConfirmation{TurnID: "turn-id", ConfirmationID: "confirmation-id", ToolName: "stop_object", State: []byte(state)}
	service := conversationServiceFuncs{
		prepare: func(context.Context, auth.Identity, string, string) (conversation.TurnExecution, error) {
			return &testTurnExecution{run: func(_ context.Context, emit agent.EmitFunc) error {
				return emit(agent.Event{Type: agent.EventConfirmationRequired, Iteration: 1, Confirmation: &agent.Confirmation{
					ID: "confirmation-id", TurnID: "turn-id", ToolName: "stop_object", ToolTitle: "Stop object", Destructive: true,
					ToolCallID: "call-7", Arguments: json.RawMessage(`{"path":"web"}`), ExpiresAt: confirmationExpiry,
				}})
			}}, nil
		},
		pending: func(context.Context, auth.Identity, string) (*conversation.PendingConfirmation, error) {
			return pending, nil
		},
	}
	response := runRequest(t, service, `{"threadId":"conversation-id","runId":"run-1","messages":[{"id":"m1","role":"user","content":"stop web"}]}`)
	events := aguiEvents(t, response.Body.String())
	finished := events[len(events)-1]
	interrupt := finished["outcome"].(map[string]any)["interrupts"].([]any)[0].(map[string]any)
	if eventTypes(events) != "RUN_STARTED,TOOL_CALL_START,TOOL_CALL_ARGS,TOOL_CALL_END,RUN_FINISHED" || interrupt["id"] != "confirmation-id" || interrupt["toolCallId"] != "call-7" {
		t.Fatalf("got %s", response.Body.String())
	}

	for _, test := range []struct {
		resume   string
		decision agent.Decision
		want     string
	}{
		{`{"interruptId":"confirmation-id","status":"resolved","payload":{"approved":true}}`, agent.DecisionApprove, "RUN_STARTED,TOOL_CALL_RESULT,RUN_FINISHED"},
		{`{"interruptId":"confirmation-id","status":"resolved","payload":{"approved":false}}`, agent.DecisionReject, "RUN_STARTED,TOOL_CALL_RESULT,RUN_FINISHED"},
		{`{"interruptId":"confirmation-id","status":"cancelled"}`, agent.DecisionReject, "RUN_STARTED,TOOL_CALL_RESULT,RUN_FINISHED"},
	} {
		service.confirm = func(_ context.Context, _ auth.Identity, id, turnID, confirmationID string, decision agent.Decision) (conversation.TurnExecution, error) {
			if id != "conversation-id" || turnID != "turn-id" || confirmationID != "confirmation-id" || decision != test.decision {
				t.Fatalf("%s: confirm %q %q %q %q", test.resume, id, turnID, confirmationID, decision)
			}
			return &testTurnExecution{run: func(_ context.Context, emit agent.EmitFunc) error {
				if decision == agent.DecisionApprove {
					// The confirmed call runs: its start is not given again.
					_ = emit(agent.Event{Type: agent.EventToolStarted, ToolName: "stop_object", ToolCallID: "call-7", ToolArguments: json.RawMessage(`{}`), Iteration: 1})
					_ = emit(agent.Event{Type: agent.EventToolFinished, ToolName: "stop_object", ToolCallID: "call-7", ToolResult: json.RawMessage(`{}`), Iteration: 1})
				}
				return emit(agent.Event{Type: agent.EventCompleted, FinishReason: llm.FinishReasonCompleted, TurnID: "turn-id", Iteration: 2})
			}}, nil
		}
		response := runRequest(t, service, `{"threadId":"conversation-id","runId":"run-2","messages":[],"resume":[`+test.resume+`]}`)
		events := aguiEvents(t, response.Body.String())
		if response.Code != http.StatusOK || eventTypes(events) != test.want || events[1]["toolCallId"] != "call-7" {
			t.Fatalf("%s: got %d %s", test.resume, response.Code, response.Body.String())
		}
	}
}

func TestRunRefusesInvalidInputs(t *testing.T) {
	service := conversationServiceFuncs{
		prepare: func(context.Context, auth.Identity, string, string) (conversation.TurnExecution, error) {
			t.Fatal("an invalid run reached the agent")
			return nil, nil
		},
		pending: func(context.Context, auth.Identity, string) (*conversation.PendingConfirmation, error) {
			return &conversation.PendingConfirmation{TurnID: "turn-id", ConfirmationID: "confirmation-id", State: []byte(`{"calls":[{"ID":"c"}]}`)}, nil
		},
		confirm: func(context.Context, auth.Identity, string, string, string, agent.Decision) (conversation.TurnExecution, error) {
			t.Fatal("an invalid resume reached the agent")
			return nil, nil
		},
	}
	for body, want := range map[string]string{
		`{"threadId":"other","runId":"r","messages":[{"role":"user","content":"x"}]}`:                                                                                           "thread_mismatch",
		`{"threadId":"conversation-id","runId":"","messages":[{"role":"user","content":"x"}]}`:                                                                                  "invalid_run_id",
		`{"threadId":"conversation-id","runId":"r 1","messages":[{"role":"user","content":"x"}]}`:                                                                               "invalid_run_id",
		`{"threadId":"conversation-id","runId":"r","messages":[{"role":"assistant","content":"x"}]}`:                                                                            "invalid_prompt",
		`{"threadId":"conversation-id","runId":"r","messages":[{"role":"user","content":"  "}]}`:                                                                                "invalid_prompt",
		`{"threadId":"conversation-id","runId":"r","messages":[],"resume":[{"interruptId":"other","status":"resolved","payload":{"approved":true}}]}`:                           "confirmation_not_pending",
		`{"threadId":"conversation-id","runId":"r","messages":[],"resume":[{"interruptId":"confirmation-id","status":"resolved","payload":{"approved":true,"editedArgs":{}}}]}`: "invalid_decision",
		`{"threadId":"conversation-id","runId":"r","messages":[],"resume":[{"interruptId":"a","status":"cancelled"},{"interruptId":"b","status":"cancelled"}]}`:                 "invalid_decision",
		`not json`: "invalid_request",
	} {
		response := runRequest(t, service, body)
		if response.Code < 400 || !strings.Contains(response.Body.String(), `"code":"`+want+`"`) {
			t.Errorf("%s: got %d %s", body, response.Code, response.Body.String())
		}
	}
}

func TestRunErrorEndsTheRun(t *testing.T) {
	service := conversationServiceFuncs{prepare: func(context.Context, auth.Identity, string, string) (conversation.TurnExecution, error) {
		return &testTurnExecution{run: func(_ context.Context, emit agent.EmitFunc) error {
			_ = emit(agent.Event{Type: agent.EventTextDelta, TextDelta: "partial", Iteration: 1})
			return errors.New("model failed")
		}}, nil
	}}
	response := runRequest(t, service, `{"threadId":"conversation-id","runId":"run-1","messages":[{"role":"user","content":"x"}]}`)
	events := aguiEvents(t, response.Body.String())
	if eventTypes(events) != "RUN_STARTED,TEXT_MESSAGE_START,TEXT_MESSAGE_CONTENT,TEXT_MESSAGE_END,RUN_ERROR" || events[4]["code"] != "agent_failed" {
		t.Fatalf("got %s", response.Body.String())
	}
}
