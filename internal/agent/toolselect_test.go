package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/ai-agent/internal/llm"
)

// searchCatalog is larger than the threshold of searchAgent: the model sees
// the core tools and search_tools only.
func searchCatalog() []*mcp.Tool {
	destructive := true
	tool := func(name, description string, meta mcp.Meta) *mcp.Tool {
		return &mcp.Tool{Name: name, Description: description, InputSchema: objectSchema(), Meta: meta,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}
	}
	return []*mcp.Tool{
		tool("get_cluster_status", "The status of the cluster.", mcp.Meta{CoreToolMeta: true}),
		tool("list_node_hardware", "The PCI devices and memory banks of a node: its hardware inventory.", nil),
		tool("get_node_san_topology", "The fibre channel paths of a node.", nil),
		tool("list_node_packages", "The packages installed on a node.", nil),
		{Name: "stop_object", Description: "Stop an object.", InputSchema: objectSchema(), Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive}},
	}
}

func searchAgent(t *testing.T, model llm.Client, session *fakeSession, config Config) *Agent {
	t.Helper()
	config.SystemPrompt, config.MaxIterations, config.Timeout = testSystemPrompt, 6, time.Minute
	if config.SearchThreshold == 0 {
		config.SearchThreshold = 3
	}
	agent, err := New(model, func(context.Context) (MCPSession, error) { return session, nil }, config)
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func toolNames(tools []llm.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func searchCallStep(id, name, arguments string) llmStep {
	return func(_ llm.Request, emit llm.EmitFunc) error {
		return emitEvents(emit,
			llm.Event{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(arguments)}},
			llm.Event{Type: llm.EventCompleted, FinishReason: llm.FinishReasonToolCalls})
	}
}

func textStep(text string) llmStep {
	return func(_ llm.Request, emit llm.EmitFunc) error {
		return emitEvents(emit, llm.Event{Type: llm.EventTextDelta, TextDelta: text}, llm.Event{Type: llm.EventCompleted, FinishReason: llm.FinishReasonCompleted})
	}
}

func TestSearchShowsTheCoreThenTheToolsFound(t *testing.T) {
	session := &fakeSession{tools: searchCatalog()}
	var requests []llm.Request
	record := func(step llmStep) llmStep {
		return func(request llm.Request, emit llm.EmitFunc) error {
			requests = append(requests, request)
			return step(request, emit)
		}
	}
	model := &scriptedLLM{t: t, steps: []llmStep{
		record(searchCallStep("search-1", SearchToolName, `{"query":"node hardware inventory","limit":1}`)),
		record(searchCallStep("call-1", "list_node_hardware", `{}`)),
		record(textStep("done")),
	}}
	var events []Event
	result, err := searchAgent(t, model, session, Config{}).RunTurn(t.Context(), nil, "hardware of node1", func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(requests[0].Tools); !slices.Equal(got, []string{SearchToolName, "get_cluster_status"}) {
		t.Fatalf("first tools = %v", got)
	}
	if got := toolNames(requests[1].Tools); !slices.Equal(got, []string{SearchToolName, "get_cluster_status", "list_node_hardware"}) {
		t.Fatalf("tools after the search = %v", got)
	}
	if !strings.Contains(requests[0].Messages[0].Text, searchInstruction) {
		t.Fatal("the system prompt does not explain the search")
	}
	if len(session.calls) != 1 || session.calls[0].name != "list_node_hardware" {
		t.Fatalf("MCP calls = %v: the search must not reach the MCP", session.calls)
	}
	searchResult := string(result.Messages[2].ToolResults[0].Content)
	if !strings.Contains(searchResult, `list_node_hardware`) || !strings.Contains(searchResult, `hardware inventory`) || result.Messages[2].ToolResults[0].IsError {
		t.Fatalf("search result = %s", searchResult)
	}
	if events[0].Type != EventToolStarted || events[0].ToolName != SearchToolName || events[1].Type != EventToolFinished {
		t.Fatalf("events = %+v", events)
	}
}

func TestSearchFallsBackToTheConfiguredCoreAndKeepsRecentTools(t *testing.T) {
	catalog := searchCatalog()
	catalog[0].Meta = nil
	session := &fakeSession{tools: catalog}
	history := []llm.Message{
		{Role: llm.RoleUser, Text: "packages?"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "old", Name: "list_node_packages", Arguments: json.RawMessage(`{}`)}}},
		{Role: llm.RoleTool, ToolResults: []llm.ToolResult{{CallID: "old", Content: json.RawMessage(`{}`)}}},
		{Role: llm.RoleAssistant, Text: "listed"},
	}
	var tools []string
	model := &scriptedLLM{t: t, steps: []llmStep{func(request llm.Request, emit llm.EmitFunc) error {
		tools = toolNames(request.Tools)
		return textStep("ok")(request, emit)
	}}}
	if _, err := searchAgent(t, model, session, Config{CoreTools: []string{"get_node_san_topology", "unknown_tool"}}).RunTurn(t.Context(), history, "and now?", func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tools, []string{SearchToolName, "get_node_san_topology", "list_node_packages"}) {
		t.Fatalf("tools = %v", tools)
	}
}

func TestSmallCatalogsAreShownWhole(t *testing.T) {
	for _, config := range []Config{{SearchThreshold: 10}, {SearchThreshold: -1}} {
		session := &fakeSession{tools: searchCatalog()}
		var tools []string
		var system string
		model := &scriptedLLM{t: t, steps: []llmStep{func(request llm.Request, emit llm.EmitFunc) error {
			tools, system = toolNames(request.Tools), request.Messages[0].Text
			return textStep("ok")(request, emit)
		}}}
		if _, err := searchAgent(t, model, session, config).RunTurn(t.Context(), nil, "hi", func(Event) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if len(tools) != 5 || slices.Contains(tools, SearchToolName) || strings.Contains(system, searchInstruction) {
			t.Fatalf("threshold %d: tools = %v", config.SearchThreshold, tools)
		}
	}
}

func TestSearchRefusalsAreToolErrors(t *testing.T) {
	session := &fakeSession{tools: searchCatalog()}
	var results []llm.ToolResult
	steps := []llmStep{}
	for i, arguments := range []string{`{"query":"the of"}`, `{"query":"node","limit":11}`, `{"query":"node","other":1}`, `{"query":"kubernetes pods"}`, `{"query":"node"}`} {
		steps = append(steps, searchCallStep(string(rune('a'+i)), SearchToolName, arguments))
	}
	steps = append(steps, textStep("done"))
	model := &scriptedLLM{t: t, steps: steps}
	agent := searchAgent(t, model, session, Config{})
	agent.config.MaxIterations = 8
	result, err := agent.RunTurn(t.Context(), nil, "search", func(Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range result.Messages {
		results = append(results, message.ToolResults...)
	}
	// Invalid queries, limits and arguments are refused without counting;
	// a search without match is no error; the fourth search is refused.
	wantErrors := []bool{true, true, true, false, false}
	for i, want := range wantErrors {
		if results[i].IsError != want {
			t.Fatalf("result %d = %s, error %v", i, results[i].Content, results[i].IsError)
		}
	}
	if !strings.Contains(string(results[3].Content), "No tool matches") {
		t.Fatalf("no match result = %s", results[3].Content)
	}
}

func TestSearchIsLimitedPerTurn(t *testing.T) {
	session := &fakeSession{tools: searchCatalog()}
	steps := []llmStep{}
	for i := range maxSearchesPerTurn + 1 {
		steps = append(steps, searchCallStep(string(rune('a'+i)), SearchToolName, `{"query":"node"}`))
	}
	steps = append(steps, textStep("done"))
	agent := searchAgent(t, &scriptedLLM{t: t, steps: steps}, session, Config{})
	agent.config.MaxIterations = 8
	result, err := agent.RunTurn(t.Context(), nil, "search", func(Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	last := result.Messages[2*(maxSearchesPerTurn+1)].ToolResults[0]
	if !last.IsError || !strings.Contains(string(last.Content), "limited") {
		t.Fatalf("search past the limit = %s", last.Content)
	}
}

func TestACatalogToolOutsideTheShownOnesStillRuns(t *testing.T) {
	session := &fakeSession{tools: searchCatalog()}
	model := &scriptedLLM{t: t, steps: []llmStep{searchCallStep("call-1", "list_node_packages", `{}`), textStep("done")}}
	if _, err := searchAgent(t, model, session, Config{}).RunTurn(t.Context(), nil, "packages", func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(session.calls) != 1 || session.calls[0].name != "list_node_packages" {
		t.Fatalf("calls = %v", session.calls)
	}
	model = &scriptedLLM{t: t, steps: []llmStep{searchCallStep("call-1", "no_such_tool", `{}`)}}
	if _, err := searchAgent(t, model, &fakeSession{tools: searchCatalog()}, Config{}).RunTurn(t.Context(), nil, "x", func(Event) error { return nil }); err == nil {
		t.Fatal("a tool outside the catalog ran")
	}
}

func TestAnMCPToolNamedLikeTheSearchIsRefused(t *testing.T) {
	catalog := append(searchCatalog(), &mcp.Tool{Name: SearchToolName, InputSchema: objectSchema()})
	model := &scriptedLLM{t: t}
	_, err := searchAgent(t, model, &fakeSession{tools: catalog}, Config{}).RunTurn(t.Context(), nil, "x", func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("error = %v", err)
	}
}

func TestAResumedTurnSeesTheToolsItFound(t *testing.T) {
	session := &fakeSession{tools: searchCatalog()}
	model := &scriptedLLM{t: t, steps: []llmStep{
		searchCallStep("search-1", SearchToolName, `{"query":"stop object","limit":1}`),
		searchCallStep("call-1", "stop_object", `{}`),
	}}
	agent := searchAgent(t, model, session, Config{})
	suspended, err := agent.RunTurn(t.Context(), nil, "stop it", func(Event) error { return nil })
	if err != nil || suspended.Suspended == nil {
		t.Fatalf("turn = %+v, %v", suspended, err)
	}
	state := *suspended.Suspended
	if !slices.Contains(state.ActiveTools, "stop_object") || state.Searches != 1 {
		t.Fatalf("suspended state = %+v", state)
	}
	encoded, _ := json.Marshal(state)
	var decoded SuspendedTurn
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	var tools []string
	model.steps = append(model.steps, func(request llm.Request, emit llm.EmitFunc) error {
		tools = toolNames(request.Tools)
		return textStep("stopped")(request, emit)
	})
	if _, err := agent.ResumeTurn(t.Context(), nil, decoded, DecisionApprove, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tools, "stop_object") {
		t.Fatalf("resumed tools = %v", tools)
	}
	decoded.Searches = maxSearchesPerTurn + 1
	if _, err := agent.ResumeTurn(t.Context(), nil, decoded, DecisionApprove, func(Event) error { return nil }); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("an out of range search count was resumed: %v", err)
	}
}

func TestSummary(t *testing.T) {
	if got := summary("First sentence. Second one."); got != "First sentence." {
		t.Fatalf("summary = %q", got)
	}
	if got := summary(strings.Repeat("é", 200)); len(got) > maxSearchSummaryBytes+len("…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("long summary = %q", got)
	}
}
