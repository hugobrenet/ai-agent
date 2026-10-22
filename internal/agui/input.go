package agui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// RunAgentInput is the request of an AG-UI client for one run. The agent
// reads the thread, the run, the last message of the user and the resume of
// an interrupt; the history it keeps is its own, so the other messages, the
// state, the context and the tools of the client are not read.
type RunAgentInput struct {
	ThreadID       string          `json:"threadId"`
	RunID          string          `json:"runId"`
	ParentRunID    string          `json:"parentRunId,omitempty"`
	State          json.RawMessage `json:"state,omitempty"`
	Messages       []Message       `json:"messages"`
	Tools          []Tool          `json:"tools"`
	Context        json.RawMessage `json:"context,omitempty"`
	ForwardedProps json.RawMessage `json:"forwardedProps,omitempty"`
	Resume         []ResumeEntry   `json:"resume,omitempty"`
}

type Message struct {
	ID         string          `json:"id"`
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	// Error is set on the answer of a tool of the client that failed.
	Error string `json:"error,omitempty"`
}

// Tool is a tool of the client, which the agent offers the model and the
// client runs.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// maxToolAnswersBytes bounds the answers of the client to the calls of its
// tools in one run.
const maxToolAnswersBytes = 256 << 10

// ToolAnswer is the answer of the client to a call of one of its tools.
type ToolAnswer struct {
	CallID  string
	Text    string
	IsError bool
}

// ToolAnswers gives the answers of the client the input ends with: the
// tool messages after its last message of another role, in their order,
// and the content parts the agent cannot read, skipped. An input that does
// not end with tool messages has none.
func (input RunAgentInput) ToolAnswers() ([]ToolAnswer, []string, error) {
	first := len(input.Messages)
	for first > 0 && input.Messages[first-1].Role == "tool" {
		first--
	}
	var answers []ToolAnswer
	var skipped []string
	total := 0
	for _, message := range input.Messages[first:] {
		text, parts, err := contentText(message.Content)
		if err != nil {
			return nil, nil, fmt.Errorf("tool message %q: %w", message.ToolCallID, err)
		}
		skipped = append(skipped, parts...)
		if message.ToolCallID == "" {
			return nil, nil, errors.New("a tool message must name the call it answers with toolCallId")
		}
		answer := ToolAnswer{CallID: message.ToolCallID, Text: text, IsError: message.Error != ""}
		if answer.IsError {
			answer.Text = strings.TrimSpace(message.Error + "\n" + text)
		}
		total += len(answer.Text)
		if total > maxToolAnswersBytes {
			return nil, nil, fmt.Errorf("the answers of the client exceed %d bytes", maxToolAnswersBytes)
		}
		answers = append(answers, answer)
	}
	return answers, skipped, nil
}

// contentText reads a content, a string or a list of parts: the text of its
// text parts joined by line breaks, and the types of the parts skipped.
func contentText(content json.RawMessage) (string, []string, error) {
	if len(content) == 0 {
		return "", nil, nil
	}
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text, nil, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return "", nil, errors.New("a content must be a text or a list of content parts")
	}
	var texts, skipped []string
	for _, part := range parts {
		if part.Type != "text" {
			skipped = append(skipped, part.Type)
			continue
		}
		texts = append(texts, part.Text)
	}
	return strings.Join(texts, "\n"), skipped, nil
}

// ResumeEntry answers an interrupt of the previous run.
type ResumeEntry struct {
	InterruptID string          `json:"interruptId"`
	Status      string          `json:"status"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

// The statuses of a resume entry.
const (
	ResumeResolved  = "resolved"
	ResumeCancelled = "cancelled"
)

// Prompt gives the text of the last message of the input, which must be a
// message of the user: a string, or text parts joined by line breaks. The
// parts the agent cannot read, such as images, are skipped and named, as
// the protocol has a producer do rather than fail the run; a message left
// without text is refused.
func (input RunAgentInput) Prompt() (string, []string, error) {
	if len(input.Messages) == 0 {
		return "", nil, errors.New("messages must end with a message of the user")
	}
	last := input.Messages[len(input.Messages)-1]
	if last.Role != "user" {
		return "", nil, fmt.Errorf("the last message must be a message of the user, not %q", last.Role)
	}
	text, skipped, err := contentText(last.Content)
	if err != nil {
		return "", nil, fmt.Errorf("the message of the user: %w", err)
	}
	if text == "" && len(skipped) > 0 {
		return "", skipped, errors.New("the message of the user holds no text part, the only parts the agent reads")
	}
	return text, skipped, nil
}

// Approval reads the answer to a confirmation: approved, true or false, of
// a resolved entry; a cancelled one rejects the call. Edited arguments are
// refused, not ignored: the call that runs is the call the user saw.
func (entry ResumeEntry) Approval() (bool, error) {
	switch entry.Status {
	case ResumeCancelled:
		return false, nil
	case ResumeResolved:
		var payload struct {
			Approved *bool `json:"approved"`
		}
		decoder := json.NewDecoder(bytes.NewReader(entry.Payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil || payload.Approved == nil {
			return false, errors.New(`a resolved confirmation takes the payload {"approved": true} or {"approved": false}`)
		}
		return *payload.Approved, nil
	}
	return false, fmt.Errorf("resume status must be %q or %q", ResumeResolved, ResumeCancelled)
}
