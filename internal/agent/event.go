package agent

import (
	"encoding/json"
	"fmt"

	"github.com/opensvc/ai-agent/internal/llm"
)

type EventType string

const (
	EventTextDelta    EventType = "text_delta"
	EventToolStarted  EventType = "tool_started"
	EventToolFinished EventType = "tool_finished"
	EventUsage        EventType = "usage"
	EventCompleted    EventType = "completed"
	// EventConfirmationRequired ends a turn that waits for the user to
	// confirm a tool call. The conversation service emits it once the
	// suspended turn is stored.
	EventConfirmationRequired EventType = "confirmation_required"
	// EventClientToolsPending ends a turn that waits for the client to
	// answer the calls of its tools, once the suspended turn is stored.
	EventClientToolsPending EventType = "client_tools_pending"
)

type Event struct {
	Type      EventType
	TextDelta string
	ToolName  string
	ToolError bool
	// ToolCallID, ToolArguments and ToolResult describe a tool event: the
	// call of the model, its JSON arguments, and with tool_finished its
	// JSON result, for a client to show them.
	ToolCallID    string
	ToolArguments json.RawMessage
	ToolResult    json.RawMessage
	Usage         *llm.Usage
	FinishReason  llm.FinishReason
	Confirmation  *Confirmation
	Iteration     int
	// TurnID is set on the completion of a stored turn, for the client to
	// refer to it, such as with a feedback.
	TurnID string
	// PendingCalls are the calls of tools of the client the turn waits on,
	// in the order they were made.
	PendingCalls []string
}

type EmitFunc func(Event) error

func (e Event) Validate() error {
	if e.Iteration <= 0 {
		return fmt.Errorf("agent event iteration must be positive")
	}
	switch e.Type {
	case EventTextDelta:
		if e.TextDelta == "" || e.ToolName != "" || e.Usage != nil || e.FinishReason != "" {
			return fmt.Errorf("invalid text delta event")
		}
	case EventToolStarted:
		if e.ToolName == "" || e.ToolCallID == "" || e.TextDelta != "" || e.Usage != nil || e.FinishReason != "" || e.ToolError {
			return fmt.Errorf("invalid tool started event")
		}
	case EventToolFinished:
		if e.ToolName == "" || e.ToolCallID == "" || e.TextDelta != "" || e.Usage != nil || e.FinishReason != "" {
			return fmt.Errorf("invalid tool finished event")
		}
	case EventUsage:
		if e.Usage == nil || e.TextDelta != "" || e.ToolName != "" || e.FinishReason != "" {
			return fmt.Errorf("invalid usage event")
		}
	case EventCompleted:
		if e.FinishReason == "" || e.TextDelta != "" || e.ToolName != "" || e.Usage != nil || e.ToolError {
			return fmt.Errorf("invalid completed event")
		}
	case EventConfirmationRequired:
		c := e.Confirmation
		if c == nil || c.ID == "" || c.TurnID == "" || c.ToolName == "" || c.ToolCallID == "" || c.ExpiresAt.IsZero() || len(c.Arguments) == 0 ||
			e.TextDelta != "" || e.ToolName != "" || e.Usage != nil || e.FinishReason != "" || e.ToolError {
			return fmt.Errorf("invalid confirmation required event")
		}
	case EventClientToolsPending:
		if len(e.PendingCalls) == 0 || e.TextDelta != "" || e.ToolName != "" || e.Usage != nil || e.FinishReason != "" || e.ToolError {
			return fmt.Errorf("invalid client tools pending event")
		}
	default:
		return fmt.Errorf("unsupported agent event type %q", e.Type)
	}
	return nil
}
