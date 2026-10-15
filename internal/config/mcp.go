package config

import (
	"fmt"
	"os"
	"strings"
)

// maxUnixSocketPathBytes is the portable sun_path limit, including the NUL.
const maxUnixSocketPathBytes = 103

// MCPConfig holds the MCP socket of each backend. om3 is required; oc3 is
// served only when its socket is configured.
type MCPConfig struct {
	OM3SocketPath string
	OC3SocketPath string
}

func LoadMCP() (MCPConfig, error) {
	return loadMCP(os.Getenv)
}

func loadMCP(getenv func(string) string) (MCPConfig, error) {
	om3, err := ParseMCPSocket(getenv("OPENSVC_AI_OM3_MCP_SOCKET"))
	if err != nil {
		return MCPConfig{}, fmt.Errorf("parse OPENSVC_AI_OM3_MCP_SOCKET: %w", err)
	}
	cfg := MCPConfig{OM3SocketPath: om3}
	if value := strings.TrimSpace(getenv("OPENSVC_AI_OC3_MCP_SOCKET")); value != "" {
		if cfg.OC3SocketPath, err = ParseMCPSocket(value); err != nil {
			return MCPConfig{}, fmt.Errorf("parse OPENSVC_AI_OC3_MCP_SOCKET: %w", err)
		}
	}
	return cfg, nil
}

// ParseMCPSocket requires an absolute Unix socket path. An MCP accepts
// OpenID Connect access tokens only on its local socket. The socket need not exist yet: it
// is dialed per request, so MCP may start after the agent.
func ParseMCPSocket(value string) (string, error) {
	path, err := absoluteFile(value)
	if err != nil {
		return "", err
	}
	if len(path) > maxUnixSocketPathBytes {
		return "", fmt.Errorf("socket path exceeds %d bytes", maxUnixSocketPathBytes)
	}
	return path, nil
}
