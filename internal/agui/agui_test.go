package agui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/opensvc/ai-agent/internal/agent"
	"github.com/opensvc/ai-agent/internal/llm"
)

func types(events []Event) string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Type)
	}
	return strings.Join(names, ",")
}

func encodeAll(t *testing.T, encoder *Encoder, events ...agent.Event) []Event {
	t.Helper()
	out := encoder.Start()
	for _, event := range events {
		encoded, err := encoder.Encode(event)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, encoded...)
	}
	return out
}

func TestEncoderOrdersATurn(t *testing.T) {
	encoder := NewEncoder("c-1", "r-1")
	events := encodeAll(t, encoder,
		agent.Event{Type: agent.EventTextDelta, TextDelta: "Let me ", Iteration: 1},
		agent.Event{Type: agent.EventTextDelta, TextDelta: "look.", Iteration: 1},
		agent.Event{Type: agent.EventToolStarted, ToolName: "list_nodes", ToolCallID: "call-1", ToolArguments: json.RawMessage(`{"limit":5}`), Iteration: 1},
		agent.Event{Type: agent.EventToolFinished, ToolName: "list_nodes", ToolCallID: "call-1", ToolResult: json.RawMessage(`{"nodes":[]}`), Iteration: 1},
		agent.Event{Type: agent.EventUsage, Usage: &llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}, Iteration: 1},
		agent.Event{Type: agent.EventTextDelta, TextDelta: "No node.", Iteration: 2},
		agent.Event{Type: agent.EventCompleted, TurnID: "t-1", Iteration: 2},
	)
	want := "RUN_STARTED,TEXT_MESSAGE_START,TEXT_MESSAGE_CONTENT,TEXT_MESSAGE_CONTENT,TEXT_MESSAGE_END,TOOL_CALL_START,TOOL_CALL_ARGS,TOOL_CALL_END,TOOL_CALL_RESULT,TEXT_MESSAGE_START,TEXT_MESSAGE_CONTENT,TEXT_MESSAGE_END,RUN_FINISHED"
	if got := types(events); got != want {
		t.Fatalf("events = %s", got)
	}
	start, args, result, second, finished := events[1], events[6], events[8], events[9], events[12]
	if start.MessageID != "r-1-1" || start.Role != "assistant" || events[5].ParentMessageID != "r-1-1" || events[5].ToolCallName != "list_nodes" ||
		args.Delta != `{"limit":5}` || *result.Content != `{"nodes":[]}` || result.ToolCallID != "call-1" || second.MessageID != "r-1-2" ||
		finished.Outcome.Type != "success" || finished.Result.(map[string]string)["turnId"] != "t-1" || finished.ThreadID != "c-1" || finished.RunID != "r-1" ||
		events[0].ProtocolVersion != "1.0" || len(finished.Usage) != 1 || finished.Usage[0] != (Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}) {
		t.Fatalf("events = %+v", events)
	}
	if _, err := encoder.Encode(agent.Event{Type: agent.EventTextDelta, TextDelta: "late", Iteration: 3}); err == nil {
		t.Fatal("an event after RUN_FINISHED was encoded")
	}
	if failure := encoder.Failure("agent_failed", "x"); len(failure) != 0 {
		t.Fatalf("failure after the end = %+v", failure)
	}
}

func TestEncoderInterruptsOnAConfirmation(t *testing.T) {
	encoder := NewEncoder("c-1", "r-1")
	expires := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	events := encodeAll(t, encoder,
		agent.Event{Type: agent.EventTextDelta, TextDelta: "Stopping.", Iteration: 1},
		agent.Event{Type: agent.EventConfirmationRequired, Iteration: 1, Confirmation: &agent.Confirmation{
			ID: "conf-1", TurnID: "t-1", ToolName: "stop_service", ToolTitle: "Stop service", Destructive: true, ToolCallID: "call-7",
			Arguments: json.RawMessage(`{"service":"web"}`), ArgumentTypes: map[string]string{"yaml": "application/yaml"}, ExpiresAt: expires,
		}},
	)
	if got := types(events); got != "RUN_STARTED,TEXT_MESSAGE_START,TEXT_MESSAGE_CONTENT,TEXT_MESSAGE_END,TOOL_CALL_START,TOOL_CALL_ARGS,TOOL_CALL_END,RUN_FINISHED" {
		t.Fatalf("events = %s", got)
	}
	finished := events[len(events)-1]
	interrupt := finished.Outcome.Interrupts[0]
	if finished.Outcome.Type != "interrupt" || interrupt.ID != "conf-1" || interrupt.Reason != "tool_call" || finished.Usage != nil || interrupt.ToolCallID != "call-7" ||
		interrupt.ExpiresAt != "2026-10-22T10:00:00Z" || interrupt.Metadata["turnId"] != "t-1" || interrupt.ResponseSchema["required"].([]string)[0] != "approved" ||
		events[5].Delta != `{"service":"web"}` || events[4].ToolCallID != "call-7" {
		t.Fatalf("finished = %+v", finished)
	}
	data, _ := json.Marshal(finished)
	if !strings.Contains(string(data), `"argumentTypes":{"yaml":"application/yaml"}`) {
		t.Fatalf("finished = %s", data)
	}
}

func TestEncoderResumesAConfirmedCall(t *testing.T) {
	encoder := NewEncoder("c-1", "r-2")
	encoder.Resume("call-7")
	events := encodeAll(t, encoder,
		agent.Event{Type: agent.EventToolStarted, ToolName: "stop_service", ToolCallID: "call-7", ToolArguments: json.RawMessage(`{}`), Iteration: 1},
		agent.Event{Type: agent.EventToolFinished, ToolName: "stop_service", ToolCallID: "call-7", ToolResult: json.RawMessage(`{"ok":true}`), Iteration: 1},
		agent.Event{Type: agent.EventToolStarted, ToolName: "get_service", ToolCallID: "call-8", ToolArguments: json.RawMessage(`{}`), Iteration: 2},
	)
	// The confirmed call reports its result only; the next call is whole.
	if got := types(events); got != "RUN_STARTED,TOOL_CALL_RESULT,TOOL_CALL_START,TOOL_CALL_ARGS,TOOL_CALL_END" {
		t.Fatalf("events = %s", got)
	}
	rejected := NewEncoder("c-1", "r-3").Rejected("call-7")
	if len(rejected) != 1 || rejected[0].Type != ToolCallResult || rejected[0].ToolCallID != "call-7" || !strings.Contains(*rejected[0].Content, "rejected") {
		t.Fatalf("rejected = %+v", rejected)
	}
}

func TestEncoderFailureClosesTheText(t *testing.T) {
	encoder := NewEncoder("c-1", "r-1")
	events := encodeAll(t, encoder, agent.Event{Type: agent.EventTextDelta, TextDelta: "partial", Iteration: 1})
	events = append(events, encoder.Failure("request_timeout", "the agent request timed out")...)
	if got := types(events); got != "RUN_STARTED,TEXT_MESSAGE_START,TEXT_MESSAGE_CONTENT,TEXT_MESSAGE_END,RUN_ERROR" {
		t.Fatalf("events = %s", got)
	}
	if last := events[len(events)-1]; last.Code != "request_timeout" || last.Message == "" {
		t.Fatalf("error = %+v", last)
	}
}

func TestToolResultIsBounded(t *testing.T) {
	large := json.RawMessage(`"` + strings.Repeat("a", maxToolResultBytes) + `"`)
	if got := toolResult(large); !strings.Contains(got, `"omitted":true`) {
		t.Fatalf("large result = %.80s", got)
	}
	if got := toolResult(nil); got != "{}" {
		t.Fatalf("empty result = %s", got)
	}
}

func TestRunAgentInputPromptAndApproval(t *testing.T) {
	for raw, want := range map[string]string{
		`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","value":"https://x"}},{"type":"text","text":"what is it?"}]}]}`: "what is it?",
		`{"messages":[{"role":"assistant","content":"hi"},{"role":"user","content":"list nodes"}]}`:                                                    "list nodes",
		`{"messages":[{"role":"user","content":[{"type":"text","text":"list"},{"type":"text","text":"nodes"}]}]}`:                                      "list\nnodes",
		`{"messages":[{"role":"user","content":"a"},{"role":"tool","toolCallId":"x","content":"{}"}]}`:                                                 "",
		`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","value":"https://x"}}]}]}`:                                      "",
		`{"messages":[]}`: "",
	} {
		var input RunAgentInput
		if err := json.Unmarshal([]byte(raw), &input); err != nil {
			t.Fatal(err)
		}
		prompt, skipped, err := input.Prompt()
		if (want == "") != (err != nil) || prompt != want || (strings.Contains(raw, "image") && (len(skipped) != 1 || skipped[0] != "image")) {
			t.Errorf("%s: prompt %q, %v", raw, prompt, err)
		}
	}
	for raw, want := range map[string]string{
		`{"interruptId":"c","status":"resolved","payload":{"approved":true}}`:                      "true",
		`{"interruptId":"c","status":"resolved","payload":{"approved":false}}`:                     "false",
		`{"interruptId":"c","status":"cancelled"}`:                                                 "false",
		`{"interruptId":"c","status":"resolved","payload":{"approved":true,"editedArgs":{"a":1}}}`: "error",
		`{"interruptId":"c","status":"resolved","payload":{}}`:                                     "error",
		`{"interruptId":"c","status":"later"}`:                                                     "error",
	} {
		var entry ResumeEntry
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			t.Fatal(err)
		}
		approved, err := entry.Approval()
		got := map[bool]string{true: "true", false: "false"}[approved]
		if err != nil {
			got = "error"
		}
		if got != want {
			t.Errorf("%s: %s", raw, got)
		}
	}
}
