package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/ai-agent/internal/llm"
)

var pageTool = ClientTool{Name: "open_page", Description: "Open a page of the application", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}

// The calls of tools of the client go to the client at once, the calls of
// the MCP before them run, those after them run once the client answered.
func TestClientToolCallsSuspendTheTurnUntilAnswered(t *testing.T) {
	session := &fakeSession{tools: []*mcp.Tool{{Name: "get_cluster_health", InputSchema: objectSchema(), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}}}
	var offered []llm.Tool
	model := &scriptedLLM{t: t, steps: []llmStep{func(request llm.Request, emit llm.EmitFunc) error {
		offered = request.Tools
		return callsStep(
			llm.ToolCall{ID: "mcp-1", Name: "get_cluster_health", Arguments: json.RawMessage(`{}`)},
			llm.ToolCall{ID: "ui-1", Name: "open_page", Arguments: json.RawMessage(`{"path":"/nodes"}`)},
			llm.ToolCall{ID: "mcp-2", Name: "get_cluster_health", Arguments: json.RawMessage(`{}`)},
			llm.ToolCall{ID: "ui-2", Name: "open_page", Arguments: json.RawMessage(`{"path":"/services"}`)},
		)(request, emit)
	}}}
	agent := newTestAgent(t, model, session, 4)
	ctx := WithClientTools(t.Context(), []ClientTool{pageTool})
	var events []Event
	result, err := agent.RunTurn(ctx, nil, "show the nodes", func(event Event) error { events = append(events, event); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(offered, func(tool llm.Tool) bool { return tool.Name == "open_page" }) {
		t.Fatalf("offered tools = %v", offered)
	}
	state := result.Suspended
	if state == nil || !state.WaitsOnClient() || !slices.Equal(state.ClientCalls, []string{"ui-1", "ui-2"}) || len(session.calls) != 1 || len(state.Results) != 1 {
		t.Fatalf("result = %+v, calls = %v", result, session.calls)
	}
	var started []string
	for _, event := range events {
		if event.Type == EventToolStarted {
			started = append(started, event.ToolCallID)
		}
	}
	if !slices.Equal(started, []string{"mcp-1", "ui-1", "ui-2"}) {
		t.Fatalf("started = %v", started)
	}

	// The client answers both calls: the call of the MCP between them runs,
	// and the model reads the answers in the order of the calls.
	var answers []llm.ToolResult
	model.steps = append(model.steps, func(request llm.Request, emit llm.EmitFunc) error {
		answers = request.Messages[len(request.Messages)-1].ToolResults
		return textStep("shown")(request, emit)
	})
	resumed := *state
	resumed.ClientResults = []llm.ToolResult{ClientToolResult("ui-2", "opened", false), ClientToolResult("ui-1", "the page failed", true)}
	result, err = agent.ResumeTurn(ctx, nil, resumed, "", func(Event) error { return nil })
	if err != nil || result.Suspended != nil {
		t.Fatalf("resume = %+v, %v", result, err)
	}
	if len(session.calls) != 2 || len(answers) != 4 || answers[1].CallID != "ui-1" || !answers[1].IsError || answers[3].CallID != "ui-2" || answers[3].IsError {
		t.Fatalf("calls = %v, answers = %+v", session.calls, answers)
	}
	// A turn waiting on the client takes no decision, and one missing an
	// answer fails.
	if _, err := agent.ResumeTurn(ctx, nil, *state, DecisionApprove, func(Event) error { return nil }); err == nil {
		t.Fatal("a decision resumed a turn waiting on the client")
	}
	partial := *state
	partial.ClientResults = []llm.ToolResult{ClientToolResult("ui-1", "opened", false)}
	model.steps = append(model.steps, textStep("unused"))
	if _, err := agent.ResumeTurn(ctx, nil, partial, "", func(Event) error { return nil }); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("partial answers: %v", err)
	}
}

func TestAClientToolNamedAsAToolOfTheAgentIsRefused(t *testing.T) {
	for _, name := range []string{"get_cluster_health", SearchToolName, ReadResourceToolName} {
		session := &fakeSession{tools: []*mcp.Tool{{Name: "get_cluster_health", InputSchema: objectSchema()}}}
		agent := newTestAgent(t, &scriptedLLM{t: t}, session, 3)
		ctx := WithClientTools(context.Background(), []ClientTool{{Name: name}})
		if _, err := agent.RunTurn(ctx, nil, "x", func(Event) error { return nil }); !errors.Is(err, ErrClientToolConflict) {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
}
