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
}

type Tool struct {
	Name string `json:"name"`
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
	var text string
	if json.Unmarshal(last.Content, &text) == nil {
		return text, nil, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(last.Content, &parts); err != nil {
		return "", nil, errors.New("the content of the message of the user must be a text or a list of content parts")
	}
	var texts, skipped []string
	for _, part := range parts {
		if part.Type != "text" {
			skipped = append(skipped, part.Type)
			continue
		}
		texts = append(texts, part.Text)
	}
	if len(texts) == 0 {
		return "", skipped, errors.New("the message of the user holds no text part, the only parts the agent reads")
	}
	return strings.Join(texts, "\n"), skipped, nil
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
