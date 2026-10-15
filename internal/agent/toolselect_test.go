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
	"github.com/opensvc/ai-agent/internal/toolsearch"
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
	// a search without match is no error and counts.
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

// A catalog of few tools too large for one model request is searched, and
// the tools shown stay within the size of a request.
func TestALargeCatalogIsSearchedWithinTheRequestSize(t *testing.T) {
	catalog := modelCatalogTools()
	for _, tool := range catalog {
		tool.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true}
	}
	if _, _, err := convertTools(catalog); err != nil {
		t.Fatalf("the discovered catalog was refused: %v", err)
	}
	var sizes []int
	measure := func(request llm.Request) {
		total := 0
		for _, tool := range request.Tools {
			total += toolBytes(tool)
		}
		sizes = append(sizes, total)
	}
	steps := []llmStep{}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		steps = append(steps, func(request llm.Request, emit llm.EmitFunc) error {
			measure(request)
			return searchCallStep("search-"+name, SearchToolName, `{"query":"`+name+` s","limit":10}`)(request, emit)
		})
	}
	steps = append(steps[:maxSearchesPerTurn], func(request llm.Request, emit llm.EmitFunc) error {
		measure(request)
		return textStep("done")(request, emit)
	})
	agent := searchAgent(t, &scriptedLLM{t: t, steps: steps}, &fakeSession{tools: catalog}, Config{SearchThreshold: 100})
	if _, err := agent.RunTurn(t.Context(), nil, "x", func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, size := range sizes {
		if size > maxModelToolCatalogBytes {
			t.Fatalf("a request held %d bytes of tools", size)
		}
	}
}

// A search refused for its query or limit does not count against the
// searches of the turn.
func TestInvalidSearchesDoNotCount(t *testing.T) {
	session := &fakeSession{tools: searchCatalog()}
	steps := []llmStep{}
	for i := range maxSearchesPerTurn {
		steps = append(steps, searchCallStep(string(rune('a'+i)), SearchToolName, `{"query":"the of and"}`))
	}
	steps = append(steps, searchCallStep("valid", SearchToolName, `{"query":"hardware"}`), textStep("done"))
	agent := searchAgent(t, &scriptedLLM{t: t, steps: steps}, session, Config{})
	agent.config.MaxIterations = 8
	result, err := agent.RunTurn(t.Context(), nil, "x", func(Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	valid := result.Messages[2*(maxSearchesPerTurn+1)-1+1].ToolResults[0]
	if valid.IsError || !strings.Contains(string(valid.Content), "list_node_hardware") {
		t.Fatalf("the valid search after invalid ones = %s", valid.Content)
	}
}

// The arguments of every call, search_tools included, are bounded as a
// turn stores them.
func TestSearchArgumentsAreBounded(t *testing.T) {
	huge := `{"query":"` + strings.Repeat("a", maxToolArguments) + `"}`
	model := &scriptedLLM{t: t, steps: []llmStep{searchCallStep("big", SearchToolName, huge)}}
	_, err := searchAgent(t, model, &fakeSession{tools: searchCatalog()}, Config{}).RunTurn(t.Context(), nil, "x", func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "arguments exceed") {
		t.Fatalf("error = %v", err)
	}
}

// A turn suspended while searching keeps searching when it resumes, even if
// the search would no longer be on: its planned search call runs.
func TestAResumedTurnKeepsSearching(t *testing.T) {
	session := &fakeSession{tools: searchCatalog()}
	model := &scriptedLLM{t: t, steps: []llmStep{func(_ llm.Request, emit llm.EmitFunc) error {
		return emitEvents(emit,
			llm.Event{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "stop", Name: "stop_object", Arguments: json.RawMessage(`{}`)}},
			llm.Event{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "search", Name: SearchToolName, Arguments: json.RawMessage(`{"query":"hardware"}`)}},
			llm.Event{Type: llm.EventCompleted, FinishReason: llm.FinishReasonToolCalls})
	}}}
	agent := searchAgent(t, model, session, Config{})
	suspended, err := agent.RunTurn(t.Context(), nil, "stop then search", func(Event) error { return nil })
	if err != nil || suspended.Suspended == nil || !suspended.Suspended.ToolSearch {
		t.Fatalf("turn = %+v, %v", suspended.Suspended, err)
	}
	if !suspended.Exposure.ToolSearch || !slices.Contains(suspended.Exposure.ShownTools, SearchToolName) || len(suspended.Exposure.SystemPromptSHA256) != 64 {
		t.Fatalf("exposure = %+v", suspended.Exposure)
	}
	// The search is now off: the threshold exceeds the catalog.
	agent.config.SearchThreshold = 100
	var searchResult string
	model.steps = append(model.steps, func(request llm.Request, emit llm.EmitFunc) error {
		last := request.Messages[len(request.Messages)-1]
		searchResult = string(last.ToolResults[1].Content)
		return textStep("done")(request, emit)
	})
	resumed, err := agent.ResumeTurn(t.Context(), nil, *suspended.Suspended, DecisionApprove, func(Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(searchResult, "list_node_hardware") || !resumed.Exposure.ToolSearch {
		t.Fatalf("resumed search = %s, exposure %+v", searchResult, resumed.Exposure)
	}
}

// The hash of the system prompt is the one of the prompt sent, with the
// search instruction when the catalog is searched.
func TestExposureHashesThePromptSent(t *testing.T) {
	run := func(config Config) Exposure {
		model := &scriptedLLM{t: t, steps: []llmStep{textStep("ok")}}
		result, err := searchAgent(t, model, &fakeSession{tools: searchCatalog()}, config).RunTurn(t.Context(), nil, "x", func(Event) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		return result.Exposure
	}
	searched, whole := run(Config{}), run(Config{SearchThreshold: 100})
	if !searched.ToolSearch || whole.ToolSearch || searched.SystemPromptSHA256 == whole.SystemPromptSHA256 {
		t.Fatalf("searched = %+v, whole = %+v", searched, whole)
	}
	if len(whole.ShownTools) != len(searchCatalog()) || !slices.Equal(searched.ShownTools, []string{SearchToolName, "get_cluster_status"}) {
		t.Fatalf("shown tools: searched %v, whole %v", searched.ShownTools, whole.ShownTools)
	}
}

// When the tools shown leave no room, a tool found replaces the oldest tools
// that are not core; one that cannot fit is reported, not hidden.
func TestASearchMakesRoomForTheToolsFound(t *testing.T) {
	big := func(name string) *mcp.Tool {
		return &mcp.Tool{Name: name, Description: name + " tool.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			InputSchema: map[string]any{"type": "object", "description": strings.Repeat("s", 220<<10)}}
	}
	catalog := []*mcp.Tool{big("first"), big("second"), big("third"), big("fourth"), big("fifth")}
	catalog[0].Meta = mcp.Meta{CoreToolMeta: true}
	history := []llm.Message{
		{Role: llm.RoleUser, Text: "before"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "h2", Name: "second", Arguments: json.RawMessage(`{}`)}, {ID: "h3", Name: "third", Arguments: json.RawMessage(`{}`)},
			{ID: "h4", Name: "fourth", Arguments: json.RawMessage(`{}`)}}},
		{Role: llm.RoleTool, ToolResults: []llm.ToolResult{{CallID: "h2", Content: json.RawMessage(`{}`)}, {CallID: "h3", Content: json.RawMessage(`{}`)}, {CallID: "h4", Content: json.RawMessage(`{}`)}}},
		{Role: llm.RoleAssistant, Text: "done"},
	}
	var tools []string
	model := &scriptedLLM{t: t, steps: []llmStep{
		searchCallStep("search", SearchToolName, `{"query":"fifth","limit":1}`),
		func(request llm.Request, emit llm.EmitFunc) error {
			tools = toolNames(request.Tools)
			return textStep("ok")(request, emit)
		},
	}}
	result, err := searchAgent(t, model, &fakeSession{tools: catalog}, Config{}).RunTurn(t.Context(), history, "fifth", func(Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	found := string(result.Messages[2].ToolResults[0].Content)
	if !strings.Contains(found, `\"shown\":true`) || strings.Contains(found, "No tool matches") || !slices.Contains(tools, "fifth") || !slices.Contains(tools, "first") {
		t.Fatalf("found = %s, tools = %v", found, tools)
	}

	// The core tools alone leave no room: the tool found is reported as not
	// shown, with a hint, never as no match.
	for _, tool := range catalog[:4] {
		tool.Meta = mcp.Meta{CoreToolMeta: true}
	}
	model = &scriptedLLM{t: t, steps: []llmStep{searchCallStep("search", SearchToolName, `{"query":"fifth","limit":1}`), textStep("ok")}}
	result, err = searchAgent(t, model, &fakeSession{tools: catalog}, Config{}).RunTurn(t.Context(), nil, "fifth", func(Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	found = string(result.Messages[2].ToolResults[0].Content)
	if !strings.Contains(found, `\"name\":\"fifth\"`) || !strings.Contains(found, `\"shown\":false`) || !strings.Contains(found, "cannot be shown") {
		t.Fatalf("found without room = %s", found)
	}
}

// The model is told to write a few keywords, and the schema states the
// bounds of the index.
func TestSearchToolAsksForKeywords(t *testing.T) {
	var schema struct {
		Properties struct {
			Query struct {
				MinLength   int    `json:"minLength"`
				MaxLength   int    `json:"maxLength"`
				Description string `json:"description"`
			} `json:"query"`
			Limit struct {
				Maximum int `json:"maximum"`
			} `json:"limit"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(searchTool.InputSchema, &schema); err != nil {
		t.Fatalf("schema is not JSON: %v: %s", err, searchTool.InputSchema)
	}
	query := schema.Properties.Query
	if query.MinLength != 1 || query.MaxLength != 512 || schema.Properties.Limit.Maximum != maxSearchResults || !slices.Equal(schema.Required, []string{"query"}) {
		t.Fatalf("schema = %s", searchTool.InputSchema)
	}
	guidance := "Use 3–8 English keywords describing the required capability. Omit greetings, full sentences, and specific resource identifiers."
	for name, text := range map[string]string{"query": query.Description, "tool": searchTool.Description, "prompt": searchInstruction} {
		if !strings.Contains(text, guidance) {
			t.Errorf("%s description lacks the guidance: %q", name, text)
		}
	}
	if !strings.Contains(query.Description, "At most 512 UTF-8 bytes.") {
		t.Errorf("query description lacks the byte bound: %q", query.Description)
	}
	// A query of 512 characters of two bytes each fits maxLength, not the
	// bound of the index: the description gives the exact one.
	if _, err := mustIndex(t).Search(strings.Repeat("é", 512), 1); err == nil {
		t.Error("a query of 1024 UTF-8 bytes was searched")
	}
	if err := (llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "x"}}, Tools: []llm.Tool{searchTool}}).Validate(); err != nil {
		t.Fatalf("search tool is not a valid model tool: %v", err)
	}
	refused := (&toolSelection{index: mustIndex(t)}).run(llm.ToolCall{ID: "c", Name: SearchToolName, Arguments: json.RawMessage(`{"query":"the of and"}`)}, nil)
	if !refused.IsError || !strings.Contains(string(refused.Content), "3–8 English keywords") {
		t.Fatalf("refusal = %s", refused.Content)
	}
}

func mustIndex(t *testing.T) *toolsearch.Index {
	t.Helper()
	index, err := toolsearch.New([]toolsearch.Document{{Name: "list_node_hardware", Description: "hardware"}})
	if err != nil {
		t.Fatal(err)
	}
	return index
}
