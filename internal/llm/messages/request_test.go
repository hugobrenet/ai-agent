package messages

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/opensvc/ai-agent/internal/llm"
)

func TestCreateRequestPreservesTextToolsAndResults(t *testing.T) {
	request := llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Text: "instructions"},
			{Role: llm.RoleSystem, Text: "more instructions"},
			{Role: llm.RoleUser, Text: "health"},
			{Role: llm.RoleAssistant, Text: "checking", ToolCalls: []llm.ToolCall{
				{ID: "call-1", Name: "health", Arguments: json.RawMessage(`{"node":"node1"}`)},
				{ID: "call-2", Name: "health", Arguments: json.RawMessage(`{}`)},
			}},
			{Role: llm.RoleTool, ToolResults: []llm.ToolResult{{CallID: "call-1", Content: json.RawMessage(`{"status":"ok"}`)}}},
			{Role: llm.RoleTool, ToolResults: []llm.ToolResult{{CallID: "call-2", Content: json.RawMessage(`{"error":"unavailable"}`), IsError: true}}},
		},
		Tools: []llm.Tool{{Name: "health", Description: "Get health", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	wire, err := newCreateRequest("model", 512, request)
	if err != nil {
		t.Fatal(err)
	}
	if wire.Model != "model" || wire.MaxTokens != 512 || !wire.Stream || wire.System != "instructions\n\nmore instructions" {
		t.Fatalf("unexpected request: %#v", wire)
	}
	if len(wire.Tools) != 1 || string(wire.Tools[0].InputSchema) != `{"type":"object"}` {
		t.Fatalf("unexpected tools: %#v", wire.Tools)
	}
	if len(wire.Messages) != 3 || wire.Messages[1].Role != "assistant" || len(wire.Messages[1].Content) != 3 || wire.Messages[2].Role != "user" || len(wire.Messages[2].Content) != 2 {
		t.Fatalf("unexpected messages: %#v", wire.Messages)
	}
	result := wire.Messages[2].Content[1]
	if result.Type != "tool_result" || result.ToolUseID != "call-2" || !result.IsError || result.Content != `{"error":"unavailable"}` {
		t.Fatalf("unexpected result: %#v", result)
	}
	body, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"store", "thinking", "authorization", "api_key"} {
		if _, exists := fields[field]; exists {
			t.Fatalf("unexpected field %s", field)
		}
	}
}

func TestCreateRequestMergesAdjacentRoles(t *testing.T) {
	request := llm.Request{Messages: []llm.Message{
		{Role: llm.RoleUser, Text: "first"}, {Role: llm.RoleUser, Text: "second"},
		{Role: llm.RoleAssistant, Text: "one"}, {Role: llm.RoleAssistant, Text: "two"},
		{Role: llm.RoleUser, Text: "next"},
	}}
	wire, err := newCreateRequest("model", 100, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire.Messages) != 3 || len(wire.Messages[0].Content) != 2 || len(wire.Messages[1].Content) != 2 {
		t.Fatalf("messages not merged: %#v", wire.Messages)
	}
}

func TestCreateRequestRejectsUnrepresentableHistory(t *testing.T) {
	user := llm.Message{Role: llm.RoleUser, Text: "hello"}
	call := llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call", Name: "health", Arguments: json.RawMessage(`{}`)}}}
	result := llm.Message{Role: llm.RoleTool, ToolResults: []llm.ToolResult{{CallID: "call", Content: json.RawMessage(`{}`)}}}
	for name, history := range map[string][]llm.Message{
		"late system":      {user, {Role: llm.RoleSystem, Text: "late"}},
		"unmatched result": {user, result},
		"missing result":   {user, call},
		"intervening user": {user, call, user, result},
		"duplicate result": {user, call, result, result},
		"duplicate call":   {user, call, result, call, result},
		"system only":      {{Role: llm.RoleSystem, Text: "system"}},
		"assistant first":  {{Role: llm.RoleAssistant, Text: "hello"}, user},
		"prefill":          {user, {Role: llm.RoleAssistant, Text: "hello"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newCreateRequest("model", 100, llm.Request{Messages: history}); err == nil || !strings.Contains(err.Error(), "Messages") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
