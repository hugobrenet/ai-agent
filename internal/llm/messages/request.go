package messages

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hugobrenet/opensvc-ai-agent/internal/llm"
)

type createRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	Stream    bool          `json:"stream"`
	System    string        `json:"system,omitempty"`
	Messages  []wireMessage `json:"messages"`
	Tools     []wireTool    `json:"tools,omitempty"`
}

type wireMessage struct {
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}

type wireBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type wireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func newCreateRequest(model string, maxTokens int, request llm.Request) (createRequest, error) {
	result := createRequest{Model: model, MaxTokens: maxTokens, Stream: true}
	var system []string
	pending := make(map[string]struct{})
	seen := make(map[string]struct{})
	appendMessage := func(role string, blocks []wireBlock) {
		if n := len(result.Messages); n > 0 && result.Messages[n-1].Role == role {
			result.Messages[n-1].Content = append(result.Messages[n-1].Content, blocks...)
		} else {
			result.Messages = append(result.Messages, wireMessage{Role: role, Content: blocks})
		}
	}
	for index, message := range request.Messages {
		if len(pending) != 0 && message.Role != llm.RoleTool {
			return createRequest{}, fmt.Errorf("Messages history has unresolved tool calls before message %d", index)
		}
		switch message.Role {
		case llm.RoleSystem:
			if len(result.Messages) != 0 {
				return createRequest{}, fmt.Errorf("Messages system instructions must precede conversation messages")
			}
			system = append(system, message.Text)
		case llm.RoleUser:
			appendMessage("user", []wireBlock{{Type: "text", Text: message.Text}})
		case llm.RoleAssistant:
			var blocks []wireBlock
			if message.Text != "" {
				blocks = append(blocks, wireBlock{Type: "text", Text: message.Text})
			}
			for _, call := range message.ToolCalls {
				if _, exists := seen[call.ID]; exists {
					return createRequest{}, fmt.Errorf("Messages history has duplicate tool call IDs")
				}
				seen[call.ID] = struct{}{}
				pending[call.ID] = struct{}{}
				blocks = append(blocks, wireBlock{Type: "tool_use", ID: call.ID, Name: call.Name, Input: call.Arguments})
			}
			appendMessage("assistant", blocks)
		case llm.RoleTool:
			var blocks []wireBlock
			for _, toolResult := range message.ToolResults {
				if _, exists := pending[toolResult.CallID]; !exists {
					return createRequest{}, fmt.Errorf("Messages history has an unmatched or duplicate tool result")
				}
				delete(pending, toolResult.CallID)
				blocks = append(blocks, wireBlock{Type: "tool_result", ToolUseID: toolResult.CallID, Content: string(toolResult.Content), IsError: toolResult.IsError})
			}
			appendMessage("user", blocks)
		default:
			return createRequest{}, fmt.Errorf("Messages history contains an unsupported role")
		}
	}
	if len(pending) != 0 {
		return createRequest{}, fmt.Errorf("Messages history has unresolved tool calls")
	}
	if len(result.Messages) == 0 || result.Messages[0].Role != "user" || result.Messages[len(result.Messages)-1].Role != "user" {
		return createRequest{}, fmt.Errorf("Messages history must begin and end with a user message or tool results")
	}
	result.System = strings.Join(system, "\n\n")
	for _, tool := range request.Tools {
		result.Tools = append(result.Tools, wireTool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	return result, nil
}
