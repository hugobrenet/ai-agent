//go:build integration

package messages_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/hugobrenet/opensvc-ai-agent/internal/config"
	"github.com/hugobrenet/opensvc-ai-agent/internal/llm"
	"github.com/hugobrenet/opensvc-ai-agent/internal/llmfactory"
)

func TestLiveMessagesText(t *testing.T) {
	client := liveClient(t)
	var text strings.Builder
	var completed bool
	err := client.Stream(t.Context(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "Reply exactly with OK and nothing else."}}}, func(event llm.Event) error {
		if event.Type == llm.EventTextDelta {
			text.WriteString(event.TextDelta)
		}
		if event.Type == llm.EventCompleted {
			completed = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("stream live text response: %v", err)
	}
	if !completed || strings.TrimSpace(text.String()) == "" {
		t.Fatal("live response is incomplete or empty")
	}
}

func TestLiveMessagesToolCall(t *testing.T) {
	client := liveClient(t)
	user := llm.Message{Role: llm.RoleUser, Text: "Call report_health exactly once with status set to ok. After receiving the tool result, reply exactly with DONE."}
	tool := llm.Tool{Name: "report_health", Description: "Report a synthetic health status for an integration test.", InputSchema: json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","enum":["ok"]}},"required":["status"],"additionalProperties":false}`)}
	request := llm.Request{Messages: []llm.Message{user}, Tools: []llm.Tool{tool}}
	var calls []llm.ToolCall
	var finish llm.FinishReason
	err := client.Stream(t.Context(), request, func(event llm.Event) error {
		if event.Type == llm.EventToolCall {
			calls = append(calls, *event.ToolCall)
		}
		if event.Type == llm.EventCompleted {
			finish = event.FinishReason
		}
		return nil
	})
	if err != nil {
		t.Fatalf("stream live tool response: %v", err)
	}
	if len(calls) != 1 || calls[0].Name != "report_health" || finish != llm.FinishReasonToolCalls {
		t.Fatal("live response did not call the expected tool")
	}
	var args struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(calls[0].Arguments, &args); err != nil || args.Status != "ok" {
		t.Fatal("live tool arguments are invalid")
	}
	request.Messages = append(request.Messages,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: calls},
		llm.Message{Role: llm.RoleTool, ToolResults: []llm.ToolResult{{CallID: calls[0].ID, Content: json.RawMessage(`{"status":"ok"}`)}}},
	)
	var text strings.Builder
	err = client.Stream(t.Context(), request, func(event llm.Event) error {
		if event.Type == llm.EventTextDelta {
			text.WriteString(event.TextDelta)
		}
		if event.Type == llm.EventCompleted {
			finish = event.FinishReason
		}
		return nil
	})
	if err != nil {
		t.Fatalf("stream live tool result response: %v", err)
	}
	if strings.TrimSpace(text.String()) == "" || finish != llm.FinishReasonCompleted {
		t.Fatal("live tool result produced no complete final text")
	}
}

func liveClient(t *testing.T) llm.Client {
	t.Helper()
	if os.Getenv("OPENSVC_AI_LLM_PROTOCOL") != config.LLMProtocolMessages {
		t.Skip("live Messages configuration is unavailable")
	}
	processConfig, err := config.LoadLLM()
	if err != nil {
		t.Fatalf("load live LLM configuration: %v", err)
	}
	client, err := llmfactory.New(processConfig, nil)
	if err != nil {
		t.Fatalf("create live LLM client: %v", err)
	}
	return client
}
