package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/ai-agent/internal/llm"
)

// editableTool is a destructive tool whose input schema the user may edit
// its calls against.
func editableTool() *mcp.Tool {
	tool := destructiveTool("switch_object")
	tool.InputSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1},"to":{"type":"string"},"live":{"type":"boolean"}},"required":["path"],"additionalProperties":false}`)
	return tool
}

func TestEditableSchemaOfATool(t *testing.T) {
	if editableSchema(editableTool()) == nil {
		t.Fatal("an object schema takes no edits")
	}
	for name, schema := range map[string]any{
		"none":       nil,
		"not object": json.RawMessage(`{"type":"string"}`),
		"too large":  json.RawMessage(`{"type":"object","description":"` + strings.Repeat("x", maxEditableSchemaBytes) + `"}`),
		"remote ref": json.RawMessage(`{"type":"object","properties":{"a":{"$ref":"https://example.com/a.json"}}}`),
	} {
		tool := destructiveTool("x")
		tool.InputSchema = schema
		if editableSchema(tool) != nil {
			t.Errorf("%s: the call takes edits", name)
		}
	}
}

func TestValidateEditedArguments(t *testing.T) {
	state := SuspendedTurn{InputSchema: editableSchema(editableTool())}
	if err := state.ValidateEditedArguments(json.RawMessage(`{"path":"web","to":"n2"}`)); err != nil {
		t.Fatal(err)
	}
	for edited, want := range map[string]string{
		`{"to":"n2"}`:                  "missing properties",
		`{"path":"web","to":2}`:        "/properties/to",
		`{"path":"web","extra":true}`:  "additional properties",
		`["web"]`:                      "one JSON object",
		`{"path":"web"} {"path":"db"}`: "one JSON object",
	} {
		err := state.ValidateEditedArguments(json.RawMessage(edited))
		if !errors.Is(err, ErrInvalidEditedArguments) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", edited, err)
		}
	}
	if err := (SuspendedTurn{}).ValidateEditedArguments(json.RawMessage(`{}`)); !errors.Is(err, ErrInvalidEditedArguments) || !strings.Contains(err.Error(), "takes no edits") {
		t.Fatalf("a call without schema took an edit: %v", err)
	}
}

// The call runs with the arguments the user edited, a full replacement; the
// message of the model keeps the call it proposed, and the result tells the
// model of the edit, ahead of what the call returned.
func TestResumeTurnRunsTheEditedArgumentsAndTellsTheModel(t *testing.T) {
	session := &fakeSession{tools: []*mcp.Tool{editableTool()}}
	var history []llm.Message
	model := &scriptedLLM{t: t, steps: []llmStep{
		callsStep(llm.ToolCall{ID: "call-1", Name: "switch_object", Arguments: json.RawMessage(`{"path":"web","to":"n1","live":true}`)}),
		answerStep(t, func(request llm.Request) {
			history = request.Messages
		}),
	}}
	agent := newTestAgent(t, model, session, 4)
	first, err := agent.RunTurn(t.Context(), nil, "switch web", func(Event) error { return nil })
	if err != nil || first.Suspended == nil || len(first.Suspended.InputSchema) == 0 {
		t.Fatalf("got %+v %v", first.Suspended, err)
	}
	stored := *first.Suspended
	stored.EditedArguments = json.RawMessage(`{"path":"web","to":"n2"}`)
	var started Event
	result, err := agent.ResumeTurn(t.Context(), nil, stored, DecisionApprove, func(event Event) error {
		if event.Type == EventToolStarted {
			started = event
		}
		return nil
	})
	if err != nil || result.FinishReason != llm.FinishReasonCompleted {
		t.Fatalf("got %+v %v", result, err)
	}
	if len(session.calls) != 1 || session.calls[0].arguments["to"] != "n2" || session.calls[0].arguments["live"] != nil {
		t.Fatalf("the call did not run with the edited arguments, whole: %#v", session.calls)
	}
	if string(started.ToolArguments) != `{"path":"web","to":"n2"}` {
		t.Fatalf("started with %s", started.ToolArguments)
	}
	proposed, results := history[len(history)-2], history[len(history)-1]
	if string(proposed.ToolCalls[0].Arguments) != `{"path":"web","to":"n1","live":true}` {
		t.Fatalf("the message of the model was rewritten: %s", proposed.ToolCalls[0].Arguments)
	}
	var content struct {
		Content []struct{ Text string } `json:"content"`
	}
	if err := json.Unmarshal(results.ToolResults[0].Content, &content); err != nil || len(content.Content) == 0 ||
		content.Content[0].Text != `The user edited the arguments before approving; the call ran with: {"path":"web","to":"n2"}` {
		t.Fatalf("the result does not tell the edit first: %s", results.ToolResults[0].Content)
	}
	stored.InputSchema = nil
	if _, err := agent.ResumeTurn(t.Context(), nil, stored, DecisionApprove, func(Event) error { return nil }); err == nil {
		t.Fatal("a call without schema resumed with edited arguments")
	}
}
