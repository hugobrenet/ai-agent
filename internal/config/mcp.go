package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type MCPConfig struct {
	URL    string
	CAFile string
}

func LoadMCP() (MCPConfig, error) {
	return loadMCP(os.Getenv)
}

func loadMCP(getenv func(string) string) (MCPConfig, error) {
	endpoint, err := ParseMCPURL(getenv("OPENSVC_AI_MCP_URL"))
	if err != nil {
		return MCPConfig{}, fmt.Errorf("parse OPENSVC_AI_MCP_URL: %w", err)
	}
	caFile := strings.TrimSpace(getenv("OPENSVC_AI_MCP_CA_FILE"))
	if caFile != "" {
		caFile, err = absoluteFile(caFile)
		if err != nil {
			return MCPConfig{}, fmt.Errorf("parse OPENSVC_AI_MCP_CA_FILE: %w", err)
		}
	}
	return MCPConfig{URL: endpoint.String(), CAFile: caFile}, nil
}

// ParseMCPURL requires a credential-free HTTPS endpoint, including its route.
func ParseMCPURL(value string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(value))
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Opaque != "" || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
		return nil, fmt.Errorf("expected an HTTPS MCP URL without credentials, query or fragment")
	}
	if endpoint.Path == "" || endpoint.Path == "/" {
		return nil, fmt.Errorf("MCP URL must include its endpoint path (for example /mcp)")
	}
	if endpoint.Port() != "" {
		port, err := strconv.Atoi(endpoint.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid MCP URL port")
		}
	} else if strings.HasSuffix(endpoint.Host, ":") {
		return nil, fmt.Errorf("invalid MCP URL port")
	}
	return endpoint, nil
}
