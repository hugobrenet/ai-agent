package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolCatalogHash hashes the tools an MCP server listed, sorted by name, as
// they were received: name, title, description, schemas and annotations.
func toolCatalogHash(tools []*mcp.Tool) (string, error) {
	sorted := slices.Clone(tools)
	slices.SortFunc(sorted, func(a, b *mcp.Tool) int { return strings.Compare(a.Name, b.Name) })
	data, err := json.Marshal(sorted)
	if err != nil {
		return "", fmt.Errorf("encode MCP tool catalog: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
