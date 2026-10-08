package config

import (
	"fmt"
	"os"
)

// maxUnixSocketPathBytes is the portable sun_path limit, including the NUL.
const maxUnixSocketPathBytes = 103

type MCPConfig struct {
	SocketPath string
}

func LoadMCP() (MCPConfig, error) {
	return loadMCP(os.Getenv)
}

func loadMCP(getenv func(string) string) (MCPConfig, error) {
	path, err := ParseMCPSocket(getenv("OPENSVC_AI_MCP_SOCKET"))
	if err != nil {
		return MCPConfig{}, fmt.Errorf("parse OPENSVC_AI_MCP_SOCKET: %w", err)
	}
	return MCPConfig{SocketPath: path}, nil
}

// ParseMCPSocket requires an absolute Unix socket path. MCP accepts delegated
// OpenSVC tokens only on this local socket. The socket need not exist yet: it
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
