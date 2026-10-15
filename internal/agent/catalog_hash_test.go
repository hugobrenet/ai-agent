package agent

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolCatalogHashFollowsTheToolsNotTheirOrder(t *testing.T) {
	a := &mcp.Tool{Name: "a", Description: "A", InputSchema: objectSchema()}
	b := &mcp.Tool{Name: "b", Description: "B", InputSchema: objectSchema()}
	first, err := toolCatalogHash([]*mcp.Tool{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if second, _ := toolCatalogHash([]*mcp.Tool{b, a}); second != first {
		t.Fatal("tool order changed the hash")
	}
	changed := &mcp.Tool{Name: "b", Description: "B, reworded", InputSchema: objectSchema()}
	if third, _ := toolCatalogHash([]*mcp.Tool{a, changed}); third == first {
		t.Fatal("a description change kept the hash")
	}
	annotated := &mcp.Tool{Name: "b", Description: "B", InputSchema: objectSchema(), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}
	if fourth, _ := toolCatalogHash([]*mcp.Tool{a, annotated}); fourth == first {
		t.Fatal("an annotation change kept the hash")
	}
}
