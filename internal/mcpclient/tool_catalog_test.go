package mcpclient

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolCatalogBoundsCount(t *testing.T) {
	catalog := toolCatalog{}
	for index := 0; index < maxMCPToolCount; index++ {
		if err := catalog.add(&mcp.Tool{Name: "tool"}); err != nil {
			t.Fatalf("add tool %d: %v", index, err)
		}
	}
	if err := catalog.add(&mcp.Tool{Name: "extra"}); err == nil || !strings.Contains(err.Error(), "count exceeds") {
		t.Fatalf("extra tool error = %v, want count limit", err)
	}
}

func TestToolCatalogBoundsCompleteDefinitions(t *testing.T) {
	catalog := toolCatalog{}
	tool := &mcp.Tool{
		Name: "metadata_heavy",
		Meta: mcp.Meta{"padding": strings.Repeat("x", maxMCPToolDefinitionBytes)},
	}
	if err := catalog.add(tool); err == nil || !strings.Contains(err.Error(), "definition exceeds") {
		t.Fatalf("large definition error = %v, want definition limit", err)
	}
}

func TestToolCatalogBoundsAggregateDefinitions(t *testing.T) {
	catalog := toolCatalog{}
	for index := 0; ; index++ {
		tool := &mcp.Tool{Name: "tool", Description: strings.Repeat("x", maxMCPToolDefinitionBytes/2)}
		err := catalog.add(tool)
		if err == nil {
			continue
		}
		if !strings.Contains(err.Error(), "catalog exceeds") {
			t.Fatalf("tool %d error = %v, want aggregate catalog limit", index, err)
		}
		break
	}
}

// A catalog the agent accepts fits in one MCP response: the SDK lists up to
// 1000 tools per page.
func TestToolCatalogFitsOneResponse(t *testing.T) {
	if int64(maxMCPToolCatalogBytes) > maxMCPResponseBodyBytes {
		t.Fatalf("catalog limit %d exceeds the response limit %d", maxMCPToolCatalogBytes, maxMCPResponseBodyBytes)
	}
}
