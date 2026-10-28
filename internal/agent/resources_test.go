package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/ai-agent/internal/auth"
	"github.com/opensvc/ai-agent/internal/llm"
)

func formDefinitionSession() *fakeSession {
	return &fakeSession{
		tools: []*mcp.Tool{{Name: "get_form", InputSchema: objectSchema()}},
		resources: []*mcp.Resource{
			{URI: "oc3://docs/form-definition", Name: "form-definition", MIMEType: "text/markdown", Description: "The YAML of a form."},
			{URI: "oc3://images/logo", Name: "logo", MIMEType: "image/png"},
		},
		texts: map[string]string{"oc3://docs/form-definition": "# Form definition\nInputs, Outputs."},
	}
}

// The model reads a text resource of the MCP with read_resource, offered
// in every request of the turn with the text resources listed.
func TestReadResourceGivesTheTextOfAResource(t *testing.T) {
	session := formDefinitionSession()
	var first, second llm.Request
	model := &scriptedLLM{t: t, steps: []llmStep{
		func(request llm.Request, emit llm.EmitFunc) error {
			first = request
			return callsStep(llm.ToolCall{ID: "call-1", Name: ReadResourceToolName, Arguments: json.RawMessage(`{"uri":"oc3://docs/form-definition"}`)})(request, emit)
		},
		answerStep(t, func(request llm.Request) { second = request }),
	}}
	result, err := newTestAgent(t, model, session, 4).RunTurn(auth.WithBearerToken(t.Context(), "jwt-marker"), nil, "form format", func(Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var tool *llm.Tool
	for index := range first.Tools {
		if first.Tools[index].Name == ReadResourceToolName {
			tool = &first.Tools[index]
		}
	}
	if tool == nil || !strings.Contains(tool.Description, "oc3://docs/form-definition: The YAML of a form.") || strings.Contains(tool.Description, "logo") {
		t.Fatalf("read_resource = %+v", tool)
	}
	results := lastToolResults(second)
	if len(session.reads) != 1 || len(results) != 1 || results[0].IsError || !strings.Contains(string(results[0].Content), "# Form definition") {
		t.Fatalf("reads %v, results %+v", session.reads, results)
	}
	if len(session.calls) != 0 || !strings.Contains(strings.Join(result.Exposure.ShownTools, ","), ReadResourceToolName) {
		t.Fatalf("calls %v, shown %v", session.calls, result.Exposure.ShownTools)
	}
}

// A read of a resource not listed is a tool error for the model, the turn
// going on; a server without resource offers no read_resource.
func TestReadResourceRefusesAResourceNotListed(t *testing.T) {
	session := formDefinitionSession()
	var second llm.Request
	model := &scriptedLLM{t: t, steps: []llmStep{
		callsStep(llm.ToolCall{ID: "call-1", Name: ReadResourceToolName, Arguments: json.RawMessage(`{"uri":"oc3://images/logo"}`)}),
		answerStep(t, func(request llm.Request) { second = request }),
	}}
	if _, err := newTestAgent(t, model, session, 4).RunTurn(auth.WithBearerToken(t.Context(), "jwt-marker"), nil, "logo", func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if results := lastToolResults(second); len(results) != 1 || !results[0].IsError || len(session.reads) != 0 {
		t.Fatalf("results %+v, reads %v", results, session.reads)
	}
	var tools []string
	model = &scriptedLLM{t: t, steps: []llmStep{answerStep(t, func(request llm.Request) {
		for _, tool := range request.Tools {
			tools = append(tools, tool.Name)
		}
	})}}
	if _, err := newTestAgent(t, model, &fakeSession{tools: session.tools}, 4).RunTurn(auth.WithBearerToken(t.Context(), "jwt-marker"), nil, "health", func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(tools, ","), ReadResourceToolName) {
		t.Fatalf("tools = %v", tools)
	}
}

// The description lists at most maxListedResources resources, each cut.
func TestReadResourceBoundsItsDescription(t *testing.T) {
	session := &fakeSession{}
	for index := 0; index < maxListedResources+5; index++ {
		session.resources = append(session.resources, &mcp.Resource{URI: "oc3://docs/" + strconv.Itoa(index), MIMEType: "text/markdown", Description: strings.Repeat("x", 1000)})
	}
	shelf, err := newResourceShelf(t.Context(), session, map[string]llm.Tool{}, nil)
	if err != nil || shelf.tool == nil || len(shelf.uris) != maxListedResources || len(shelf.tool.Description) > 600+maxListedResources*(maxResourceSummaryBytes+20) {
		t.Fatalf("shelf lists %d resources in %d bytes", len(shelf.uris), len(shelf.tool.Description))
	}
	// The name is the agent's: a tool of the MCP of the same name is refused.
	if _, err := newResourceShelf(t.Context(), session, map[string]llm.Tool{ReadResourceToolName: {Name: ReadResourceToolName}}, nil); err == nil {
		t.Fatal("a tool of the MCP named read_resource was accepted")
	}
	// A turn listing no resource answers a call of read_resource, such as
	// one waiting behind a confirmed call, with a tool error.
	empty, err := newResourceShelf(t.Context(), &fakeSession{}, map[string]llm.Tool{}, nil)
	if err != nil || empty.tool != nil || !empty.known(ReadResourceToolName) {
		t.Fatalf("empty shelf = %+v, %v", empty, err)
	}
	if result := empty.read(t.Context(), &fakeSession{}, llm.ToolCall{ID: "call-1", Name: ReadResourceToolName, Arguments: json.RawMessage(`{"uri":"oc3://docs/form-definition"}`)}); !result.IsError {
		t.Fatalf("read without resources = %+v", result)
	}
}
