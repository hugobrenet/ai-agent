package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/ai-agent/internal/llm"
)

func TestDecodeToolArgumentsPreservesNumbers(t *testing.T) {
	arguments, err := decodeToolArguments(llm.ToolCall{Name: "tool", Arguments: json.RawMessage(`{"value":9007199254740993}`)})
	if err != nil {
		t.Fatalf("decode arguments: %v", err)
	}
	if got := arguments["value"].(json.Number).String(); got != "9007199254740993" {
		t.Fatalf("got number %s", got)
	}
}

func TestEncodeToolResultRejectsOversizedResult(t *testing.T) {
	result := &mcp.CallToolResult{StructuredContent: map[string]any{"value": strings.Repeat("x", maxToolResult)}}
	if _, err := encodeToolResult(result); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("encodeToolResult() error = %v", err)
	}
}

func TestConvertToolsRejectsInvalidTools(t *testing.T) {
	for _, tools := range [][]*mcp.Tool{
		{nil},
		{{Name: "same", InputSchema: objectSchema()}, {Name: "same", InputSchema: objectSchema()}},
		{{Name: "bad", InputSchema: []any{}}},
	} {
		if _, _, err := convertTools(tools); err == nil {
			t.Fatalf("convertTools(%#v) succeeded", tools)
		}
	}
}

func TestConvertToolsBoundsModelVisibleDefinitions(t *testing.T) {
	for _, test := range []struct {
		name  string
		tools []*mcp.Tool
		want  string
	}{
		{
			name:  "name",
			tools: []*mcp.Tool{{Name: strings.Repeat("n", maxToolNameBytes+1), InputSchema: objectSchema()}},
			want:  "name exceeds",
		},
		{
			name:  "description",
			tools: []*mcp.Tool{{Name: "tool", Description: strings.Repeat("d", maxToolDescriptionBytes+1), InputSchema: objectSchema()}},
			want:  "description exceeds",
		},
		{
			name: "input schema",
			tools: []*mcp.Tool{{
				Name:        "tool",
				InputSchema: map[string]any{"type": "object", "description": strings.Repeat("s", maxToolInputSchemaBytes)},
			}},
			want: "input schema exceeds",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := convertTools(test.tools); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("convertTools() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func modelCatalogTools() []*mcp.Tool {
	const schemaPadding = 220 << 10
	tools := make([]*mcp.Tool, 0, 5)
	for index := 0; index < 5; index++ {
		tools = append(tools, &mcp.Tool{
			Name:        string(rune('a' + index)),
			InputSchema: map[string]any{"type": "object", "description": strings.Repeat("s", schemaPadding)},
		})
	}
	return tools
}

// decodedResult decodes a tool result as the MCP client does.
func decodedResult(t *testing.T, wire string) *mcp.CallToolResult {
	t.Helper()
	var result mcp.CallToolResult
	if err := json.Unmarshal([]byte(wire), &result); err != nil {
		t.Fatal(err)
	}
	return &result
}

func TestEncodeToolResultSendsADuplicatedStructuredContentOnce(t *testing.T) {
	// The server writes its fields in struct order; the client decodes the
	// structured content into a map whose keys sort otherwise.
	text := `{"node":"dev5n1","count":2,"disks":[{"id":"vda","size_bytes":53687091200},{"id":"vdb","size_bytes":21474836480}],"ratio":0.5,"ok":true,"none":null}`
	wire, _ := json.Marshal(map[string]any{
		"content":           []map[string]any{{"type": "text", "text": text}},
		"structuredContent": json.RawMessage(text),
	})
	encoded, err := encodeToolResult(decodedResult(t, string(wire)))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"content":[],"structuredContent":` + text + `}`; string(encoded) != want {
		t.Fatalf("encoded = %s\nwant      %s", encoded, want)
	}
}

func TestEncodeToolResultKeepsBothFormsUnlessExactlyDuplicated(t *testing.T) {
	structured := `{"node":"dev5n1","count":2}`
	result := func(contents string, structured string, isError bool) string {
		wire := `{"content":` + contents + `,"structuredContent":` + structured
		if isError {
			wire += `,"isError":true`
		}
		return wire + `}`
	}
	textOf := func(text string) string {
		quoted, _ := json.Marshal(text)
		return `{"type":"text","text":` + string(quoted) + `}`
	}
	for name, wire := range map[string]string{
		"different text":   result(`[`+textOf(`{"node":"dev5n2","count":2}`)+`]`, structured, false),
		"plain text":       result(`[`+textOf(`2 nodes`)+`]`, structured, false),
		"several contents": result(`[`+textOf(structured)+`,`+textOf(structured)+`]`, structured, false),
		"annotations":      result(`[{"type":"text","text":`+string(must(json.Marshal(structured)))+`,"annotations":{"priority":1}}]`, structured, false),
		"metadata":         result(`[{"type":"text","text":`+string(must(json.Marshal(structured)))+`,"_meta":{"k":"v"}}]`, structured, false),
		"trailing text":    result(`[`+textOf(structured+` {}`)+`]`, structured, false),
		// The decoded structured content lost the last digits of the integer:
		// the text alone holds it exactly.
		"large integer": result(`[`+textOf(`{"id":12345678901234567890}`)+`]`, `{"id":12345678901234567890}`, false),
		"exponent":      result(`[`+textOf(`{"size":1e3}`)+`]`, `{"size":1e3}`, false),
	} {
		decoded := decodedResult(t, wire)
		encoded, err := encodeToolResult(decoded)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var payload struct {
			Content           []json.RawMessage `json:"content"`
			StructuredContent json.RawMessage   `json:"structuredContent"`
		}
		if err := json.Unmarshal(encoded, &payload); err != nil || len(payload.Content) == 0 || len(payload.StructuredContent) == 0 {
			t.Fatalf("%s: one form was dropped: %s", name, encoded)
		}
		if len(decoded.Content) == 0 {
			t.Fatalf("%s: the MCP result was changed", name)
		}
	}
	// An error result holds its message as text, without structured content.
	encoded, err := encodeToolResult(decodedResult(t, `{"content":[`+textOf("node not found")+`],"isError":true}`))
	if err != nil || !strings.Contains(string(encoded), "node not found") {
		t.Fatalf("error result = %s, %v", encoded, err)
	}
	// The duplicate is dropped only from the payload, never from the result.
	duplicated := decodedResult(t, result(`[`+textOf(structured)+`]`, structured, false))
	if _, err := encodeToolResult(duplicated); err != nil || len(duplicated.Content) != 1 {
		t.Fatalf("the MCP result was changed: %+v, %v", duplicated, err)
	}
}

func must(data []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return data
}
