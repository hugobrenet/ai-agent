package mcpclient

import (
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// The catalog the MCP may list: the agent shows the model a part of
	// it when it is large, searching the rest. The MCP SDK lists up to
	// 1000 tools per page, so that a whole catalog may come in one response:
	// it is bounded as one response.
	maxMCPToolCount           = 1024
	maxMCPToolDefinitionBytes = 512 << 10
	maxMCPToolCatalogBytes    = int(maxMCPResponseBodyBytes)
)

type toolCatalog struct {
	tools        []*mcp.Tool
	encodedBytes int
}

func (c *toolCatalog) add(tool *mcp.Tool) error {
	if tool == nil {
		return fmt.Errorf("MCP tool %d is nil", len(c.tools))
	}
	if len(c.tools) >= maxMCPToolCount {
		return fmt.Errorf("MCP tool count exceeds %d", maxMCPToolCount)
	}
	encoded, err := json.Marshal(tool)
	if err != nil {
		return fmt.Errorf("encode MCP tool %d: %w", len(c.tools), err)
	}
	if len(encoded) > maxMCPToolDefinitionBytes {
		return fmt.Errorf("MCP tool %d definition exceeds %d bytes", len(c.tools), maxMCPToolDefinitionBytes)
	}
	if c.encodedBytes+len(encoded) > maxMCPToolCatalogBytes {
		return fmt.Errorf("MCP tool catalog exceeds %d bytes", maxMCPToolCatalogBytes)
	}
	c.encodedBytes += len(encoded)
	c.tools = append(c.tools, tool)
	return nil
}
