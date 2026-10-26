package agent

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/ai-agent/internal/llm"
)

// previewedTools are a write naming its preview, an action naming a preview
// that wraps its arguments, and the previews, hidden.
func previewedTools() []*mcp.Tool {
	update := destructiveTool("update_object_config")
	update.Meta = mcp.Meta{previewToolMeta: "preview_object_config_update"}
	stop := destructiveTool("stop_instance")
	stop.Meta = mcp.Meta{previewToolMeta: "preview_action", previewWrapsMeta: true}
	return []*mcp.Tool{
		{Name: "get_object_status", InputSchema: objectSchema()},
		update, stop,
		{Name: "preview_object_config_update", InputSchema: objectSchema(), Meta: mcp.Meta{hiddenToolMeta: true}},
		{Name: "preview_action", InputSchema: objectSchema(), Meta: mcp.Meta{hiddenToolMeta: true}},
	}
}

func structured(value any) *mcp.CallToolResult {
	return &mcp.CallToolResult{StructuredContent: value}
}

func TestRunTurnJoinsThePreviewToTheConfirmation(t *testing.T) {
	session := &fakeSession{tools: previewedTools(), results: map[string]*mcp.CallToolResult{
		"preview_object_config_update": structured(map[string]any{"summary": map[string]any{"key": "config_update"}}),
	}}
	model := &scriptedLLM{t: t, steps: []llmStep{func(request llm.Request, emit llm.EmitFunc) error {
		// The previews are never offered to the model.
		for _, tool := range request.Tools {
			if strings.HasPrefix(tool.Name, "preview_") {
				t.Fatalf("the model is offered %s", tool.Name)
			}
		}
		return callsStep(llm.ToolCall{ID: "call-1", Name: "update_object_config", Arguments: json.RawMessage(`{"path":"web","set":["env.a=1"]}`)})(request, emit)
	}}}
	result, err := newTestAgent(t, model, session, 4).RunTurn(t.Context(), nil, "set a", func(Event) error { return nil })
	if err != nil || result.Suspended == nil {
		t.Fatalf("result %+v, %v", result, err)
	}
	// The preview is called with the arguments of the call, which does not run.
	if len(session.calls) != 1 || session.calls[0].name != "preview_object_config_update" || session.calls[0].arguments["path"] != "web" {
		t.Fatalf("calls = %#v", session.calls)
	}
	if string(result.Suspended.Preview) != `{"summary":{"key":"config_update"}}` || result.Suspended.PreviewError != "" || result.Suspended.PreviewArguments != nil {
		t.Fatalf("preview = %s, %q", result.Suspended.Preview, result.Suspended.PreviewError)
	}
}

func TestRunTurnWrapsTheArgumentsOfAnActionPreview(t *testing.T) {
	session := &fakeSession{tools: previewedTools(), results: map[string]*mcp.CallToolResult{
		"preview_action": structured(map[string]any{"summary": map[string]any{"key": "stop_instance"}}),
	}}
	model := &scriptedLLM{t: t, steps: []llmStep{callsStep(llm.ToolCall{ID: "call-1", Name: "stop_instance", Arguments: json.RawMessage(`{"path":"web","node":"n1"}`)})}}
	result, err := newTestAgent(t, model, session, 4).RunTurn(t.Context(), nil, "stop web on n1", func(Event) error { return nil })
	if err != nil || result.Suspended == nil || len(result.Suspended.Preview) == 0 {
		t.Fatalf("result %+v, %v", result, err)
	}
	arguments, _ := session.calls[0].arguments["arguments"].(map[string]any)
	if session.calls[0].arguments["tool"] != "stop_instance" || arguments["node"] != "n1" {
		t.Fatalf("preview called with %#v", session.calls[0].arguments)
	}
}

func TestAFailedPreviewBlocksNothing(t *testing.T) {
	for name, test := range map[string]struct {
		result *mcp.CallToolResult
		err    error
		want   string
	}{
		"refused":       {result: &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "no such object"}}}, want: "refused: no such object"},
		"failed":        {err: errors.New("timeout"), want: "failed: timeout"},
		"unstructured":  {result: &mcp.CallToolResult{}, want: "no structured content"},
		"too long text": {result: &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("é", maxPreviewErrorBytes)}}}, want: "…"},
	} {
		t.Run(name, func(t *testing.T) {
			session := &fakeSession{tools: previewedTools(), results: map[string]*mcp.CallToolResult{"preview_object_config_update": test.result}, callErr: test.err}
			model := &scriptedLLM{t: t, steps: []llmStep{callsStep(llm.ToolCall{ID: "call-1", Name: "update_object_config", Arguments: json.RawMessage(`{"path":"web"}`)})}}
			result, err := newTestAgent(t, model, session, 4).RunTurn(t.Context(), nil, "set a", func(Event) error { return nil })
			if err != nil || result.Suspended == nil {
				t.Fatalf("a failed preview failed the turn: %+v, %v", result, err)
			}
			if result.Suspended.Preview != nil || !strings.Contains(result.Suspended.PreviewError, test.want) || len(result.Suspended.PreviewError) > maxPreviewErrorBytes+len("…") {
				t.Fatalf("preview %s, error %q", result.Suspended.Preview, result.Suspended.PreviewError)
			}
		})
	}
	// A preview named that is not a read-only tool of the server is not called.
	tools := previewedTools()
	tools[3] = destructiveTool("preview_object_config_update")
	session := &fakeSession{tools: tools}
	model := &scriptedLLM{t: t, steps: []llmStep{callsStep(llm.ToolCall{ID: "call-1", Name: "update_object_config", Arguments: json.RawMessage(`{"path":"web"}`)})}}
	result, err := newTestAgent(t, model, session, 4).RunTurn(t.Context(), nil, "set a", func(Event) error { return nil })
	if err != nil || len(session.calls) != 0 || !strings.Contains(result.Suspended.PreviewError, "not a read-only tool") {
		t.Fatalf("calls %#v, result %+v, %v", session.calls, result.Suspended, err)
	}
}

func TestResumeTurnPreviewsTheEditedArgumentsWithoutRunning(t *testing.T) {
	session := &fakeSession{tools: previewedTools(), results: map[string]*mcp.CallToolResult{
		"preview_object_config_update": structured(map[string]any{"summary": map[string]any{"text": "edited"}}),
	}}
	model := &scriptedLLM{t: t, steps: []llmStep{callsStep(llm.ToolCall{ID: "call-1", Name: "update_object_config", Arguments: json.RawMessage(`{"path":"web"}`)})}}
	agent := newTestAgent(t, model, session, 4)
	first, err := agent.RunTurn(t.Context(), nil, "set a", func(Event) error { return nil })
	if err != nil || first.Suspended == nil || len(first.Suspended.InputSchema) == 0 {
		t.Fatalf("first %+v, %v", first, err)
	}
	stored := *first.Suspended
	stored.EditedArguments = json.RawMessage(`{"path":"api"}`)
	var events []Event
	second, err := agent.ResumeTurn(t.Context(), nil, stored, DecisionPreview, func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil || second.Suspended == nil {
		t.Fatalf("second %+v, %v", second, err)
	}
	// The preview only: the call does not run, the model is not asked.
	if len(session.calls) != 2 || session.calls[1].name != "preview_object_config_update" || session.calls[1].arguments["path"] != "api" || len(events) != 0 {
		t.Fatalf("calls %#v, events %+v", session.calls, events)
	}
	pending, _ := second.Suspended.PendingCall()
	if string(pending.Arguments) != `{"path":"web"}` || string(second.Suspended.PreviewArguments) != `{"path":"api"}` || second.Suspended.EditedArguments != nil ||
		string(second.Suspended.Preview) != `{"summary":{"text":"edited"}}` {
		t.Fatalf("suspended = %+v", second.Suspended)
	}
	// Without edited arguments, there is nothing to preview again.
	if _, err := agent.ResumeTurn(t.Context(), nil, *first.Suspended, DecisionPreview, func(Event) error { return nil }); err == nil {
		t.Fatal("a preview without edited arguments resumed")
	}
}

func TestModelToolsLeaveOutTheHiddenAndThePreviews(t *testing.T) {
	tools := previewedTools()
	tools = append(tools, &mcp.Tool{Name: "named_preview_only"})
	tools[1].Meta[previewToolMeta] = "named_preview_only"
	var names []string
	for _, tool := range modelTools(tools) {
		names = append(names, tool.Name)
	}
	if !slices.Equal(names, []string{"get_object_status", "update_object_config", "stop_instance"}) {
		t.Fatalf("model tools = %v", names)
	}
}
