// Package agui speaks the AG-UI protocol (https://docs.ag-ui.com): it reads
// the RunAgentInput of a client and translates the events of the agent into
// AG-UI events.
package agui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/opensvc/ai-agent/internal/agent"
)

// The event types of AG-UI the agent emits.
const (
	RunStarted         = "RUN_STARTED"
	RunFinished        = "RUN_FINISHED"
	RunError           = "RUN_ERROR"
	TextMessageStart   = "TEXT_MESSAGE_START"
	TextMessageContent = "TEXT_MESSAGE_CONTENT"
	TextMessageEnd     = "TEXT_MESSAGE_END"
	ToolCallStart      = "TOOL_CALL_START"
	ToolCallArgs       = "TOOL_CALL_ARGS"
	ToolCallEnd        = "TOOL_CALL_END"
	ToolCallResult     = "TOOL_CALL_RESULT"
	Custom             = "CUSTOM"
)

// ProtocolVersion is the version of AG-UI the agent speaks, which it
// declares on RUN_STARTED.
const ProtocolVersion = "1.0"

// maxToolResultBytes bounds the result of a tool given to the client: a
// larger one is replaced by a note of its size.
const maxToolResultBytes = 64 << 10

// Event is one AG-UI event: its type, and the fields of that type.
type Event struct {
	Type            string   `json:"type"`
	Timestamp       int64    `json:"timestamp,omitempty"`
	ThreadID        string   `json:"threadId,omitempty"`
	RunID           string   `json:"runId,omitempty"`
	ProtocolVersion string   `json:"protocolVersion,omitempty"`
	MessageID       string   `json:"messageId,omitempty"`
	Role            string   `json:"role,omitempty"`
	Delta           string   `json:"delta,omitempty"`
	ToolCallID      string   `json:"toolCallId,omitempty"`
	ToolCallName    string   `json:"toolCallName,omitempty"`
	ParentMessageID string   `json:"parentMessageId,omitempty"`
	Content         *string  `json:"content,omitempty"`
	Outcome         *Outcome `json:"outcome,omitempty"`
	Result          any      `json:"result,omitempty"`
	Message         string   `json:"message,omitempty"`
	Code            string   `json:"code,omitempty"`
	Name            string   `json:"name,omitempty"`
	Value           any      `json:"value,omitempty"`
	Usage           []Usage  `json:"usage,omitempty"`
}

// Usage is the AG-UI TokenUsage of a run: the tokens of its model calls.
// totalTokens is computed, input plus output, as the protocol accounts it.
type Usage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	TotalTokens  int64 `json:"totalTokens"`
}

// Outcome is how a run finished: success, or interrupt with what the run
// waits on.
type Outcome struct {
	Type       string      `json:"type"`
	Interrupts []Interrupt `json:"interrupts,omitempty"`
	// PendingToolCallIDs are the calls of tools of the client a successful
	// run left for the client to answer, in the order they were made.
	PendingToolCallIDs []string `json:"pendingToolCallIds,omitempty"`
}

// Interrupt is a decision a run waits on: a confirmation of a tool call.
type Interrupt struct {
	ID             string         `json:"id"`
	Reason         string         `json:"reason"`
	Message        string         `json:"message,omitempty"`
	ToolCallID     string         `json:"toolCallId,omitempty"`
	ResponseSchema map[string]any `json:"responseSchema,omitempty"`
	ExpiresAt      string         `json:"expiresAt,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

// approvalSchema is the response a confirmation takes: approved or not.
var approvalSchema = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"approved": map[string]any{"type": "boolean"}},
	"required":             []string{"approved"},
	"additionalProperties": false,
}

// Encoder translates the events of one run of the agent into AG-UI events,
// in the order the protocol requires: RUN_STARTED first, a text message
// opened before its content and closed before a tool call, and RUN_FINISHED
// or RUN_ERROR last.
type Encoder struct {
	threadID string
	runID    string
	now      func() time.Time
	// message is the text message open, empty when none.
	message string
	// resumed is the tool call a resumed run reports the result of: the
	// client saw its call in the run that asked for its confirmation.
	resumed string
	ended   bool
	// usage sums the tokens of the model calls of the run, reported on its
	// terminal event; seen tells whether any call reported some.
	usage Usage
	seen  bool
}

func (e *Encoder) runUsage() []Usage {
	if !e.seen {
		return nil
	}
	usage := e.usage
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	return []Usage{usage}
}

func NewEncoder(threadID, runID string) *Encoder {
	return &Encoder{threadID: threadID, runID: runID, now: time.Now}
}

// Resume tells the encoder that the run resumes a confirmed tool call: its
// start, arguments and end are not emitted again, only its result.
func (e *Encoder) Resume(toolCallID string) { e.resumed = toolCallID }

func (e *Encoder) stamp(event Event) Event {
	event.Timestamp = e.now().UnixMilli()
	return event
}

// Start opens the run.
func (e *Encoder) Start() []Event {
	return []Event{e.stamp(Event{Type: RunStarted, ThreadID: e.threadID, RunID: e.runID, ProtocolVersion: ProtocolVersion})}
}

// Warning tells the client what the run did not use of its input, such as
// content parts the agent cannot read, which the protocol has the agent skip
// and say.
func (e *Encoder) Warning(message string, skipped []string) []Event {
	return []Event{e.stamp(Event{Type: Custom, Name: "opensvc.warning", Value: map[string]any{"message": message, "skippedContentParts": skipped}})}
}

// Rejected reports the result of a tool call the user rejected, which the
// agent does not run.
func (e *Encoder) Rejected(toolCallID string) []Event {
	content := `{"rejected":true,"message":"the user rejected this action; it did not run"}`
	return []Event{e.stamp(Event{Type: ToolCallResult, MessageID: toolCallID + "-result", ToolCallID: toolCallID, Role: "tool", Content: &content})}
}

func (e *Encoder) messageID(iteration int) string {
	return e.runID + "-" + strconv.Itoa(iteration)
}

func (e *Encoder) closeText() []Event {
	if e.message == "" {
		return nil
	}
	id := e.message
	e.message = ""
	return []Event{e.stamp(Event{Type: TextMessageEnd, MessageID: id})}
}

// Encode translates one event of the agent.
func (e *Encoder) Encode(event agent.Event) ([]Event, error) {
	if e.ended {
		return nil, fmt.Errorf("AG-UI run already finished")
	}
	switch event.Type {
	case agent.EventTextDelta:
		var events []Event
		id := e.messageID(event.Iteration)
		if e.message != id {
			events = append(events, e.closeText()...)
			e.message = id
			events = append(events, e.stamp(Event{Type: TextMessageStart, MessageID: id, Role: "assistant"}))
		}
		return append(events, e.stamp(Event{Type: TextMessageContent, MessageID: id, Delta: event.TextDelta})), nil
	case agent.EventToolStarted:
		events := e.closeText()
		if event.ToolCallID == e.resumed {
			return events, nil
		}
		return append(events, e.toolCall(event.ToolCallID, event.ToolName, event.ToolArguments, event.Iteration)...), nil
	case agent.EventToolFinished:
		content := toolResult(event.ToolResult)
		return []Event{e.stamp(Event{Type: ToolCallResult, MessageID: event.ToolCallID + "-result", ToolCallID: event.ToolCallID, Role: "tool", Content: &content})}, nil
	case agent.EventUsage:
		if event.Usage != nil {
			e.usage.InputTokens += event.Usage.InputTokens
			e.usage.OutputTokens += event.Usage.OutputTokens
			e.seen = true
		}
		return nil, nil
	case agent.EventCompleted:
		events := e.closeText()
		e.ended = true
		return append(events, e.stamp(Event{Type: RunFinished, ThreadID: e.threadID, RunID: e.runID,
			Outcome: &Outcome{Type: "success"}, Result: map[string]string{"turnId": event.TurnID}, Usage: e.runUsage()})), nil
	case agent.EventClientToolsPending:
		// The run is complete: the calls of the tools of the client, which
		// it emitted, are the client's to answer in the next run.
		events := e.closeText()
		e.ended = true
		return append(events, e.stamp(Event{Type: RunFinished, ThreadID: e.threadID, RunID: e.runID, Usage: e.runUsage(),
			Outcome: &Outcome{Type: "success", PendingToolCallIDs: slices.Clone(event.PendingCalls)}})), nil
	case agent.EventConfirmationRequired:
		c := event.Confirmation
		if c == nil {
			return nil, fmt.Errorf("AG-UI confirmation without its call")
		}
		events := e.closeText()
		// The client sees the call waiting, then the run ends on it.
		events = append(events, e.toolCall(c.ToolCallID, c.ToolName, c.Arguments, event.Iteration)...)
		title := c.ToolTitle
		if title == "" {
			title = c.ToolName
		}
		metadata := map[string]any{"turnId": c.TurnID, "tool": map[string]any{"name": c.ToolName, "title": c.ToolTitle, "destructive": c.Destructive}}
		if len(c.ArgumentTypes) > 0 {
			metadata["argumentTypes"] = c.ArgumentTypes
		}
		e.ended = true
		// tool_call is the core reason of an interrupt bound to a tool call.
		return append(events, e.stamp(Event{Type: RunFinished, ThreadID: e.threadID, RunID: e.runID, Usage: e.runUsage(), Outcome: &Outcome{Type: "interrupt", Interrupts: []Interrupt{{
			ID: c.ID, Reason: "tool_call", Message: "Confirm the action " + title + " with the arguments of its call.",
			ToolCallID: c.ToolCallID, ResponseSchema: approvalSchema, ExpiresAt: c.ExpiresAt.UTC().Format(time.RFC3339), Metadata: metadata,
		}}}})), nil
	}
	return nil, fmt.Errorf("AG-UI cannot translate agent event %q", event.Type)
}

func (e *Encoder) toolCall(id, name string, arguments json.RawMessage, iteration int) []Event {
	args := string(arguments)
	if args == "" {
		args = "{}"
	}
	parent := e.messageID(iteration)
	return []Event{
		e.stamp(Event{Type: ToolCallStart, ToolCallID: id, ToolCallName: name, ParentMessageID: parent}),
		e.stamp(Event{Type: ToolCallArgs, ToolCallID: id, Delta: args}),
		e.stamp(Event{Type: ToolCallEnd, ToolCallID: id}),
	}
}

// Failure ends a run that failed, unless it ended already.
func (e *Encoder) Failure(code, message string) []Event {
	if e.ended {
		return nil
	}
	events := e.closeText()
	e.ended = true
	return append(events, e.stamp(Event{Type: RunError, Message: message, Code: code, Usage: e.runUsage()}))
}

// toolResult gives the result of a tool to the client, as the agent gave
// it to the model, or a note of its size when it exceeds 64 KiB.
func toolResult(result json.RawMessage) string {
	if len(result) == 0 {
		return "{}"
	}
	if len(result) > maxToolResultBytes || !utf8.Valid(result) {
		return fmt.Sprintf(`{"omitted":true,"bytes":%d,"message":"the result exceeds what the agent gives the client"}`, len(result))
	}
	return string(result)
}
