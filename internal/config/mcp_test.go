package config

import "testing"

func TestLoadMCPHTTPS(t *testing.T) {
	for _, tc := range []struct {
		name, url, ca string
		wantErr       bool
	}{
		{"default missing URL", "", "", true},
		{"https", "https://mcp.example.test/mcp", "", false},
		{"explicit CA", " https://mcp.example.test:8443/mcp ", "/etc/opensvc-ai/mcp-ca.pem", false},
		{"ipv6", "https://[::1]:8443/mcp", "", false},
		{"http", "http://mcp.example.test/mcp", "", true},
		{"credentials", "https://user:pass@mcp.example.test/mcp", "", true},
		{"query", "https://mcp.example.test/mcp?token=x", "", true},
		{"fragment", "https://mcp.example.test/mcp#x", "", true},
		{"missing path", "https://mcp.example.test", "", true},
		{"relative CA", "https://mcp.example.test/mcp", "ca.pem", true},
		{"root CA path", "https://mcp.example.test/mcp", "/", true},
		{"zero port", "https://mcp.example.test:0/mcp", "", true},
		{"empty port", "https://mcp.example.test:/mcp", "", true},
		{"invalid port", "https://mcp.example.test:65536/mcp", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadMCP(func(key string) string {
				switch key {
				case "OPENSVC_AI_MCP_URL":
					return tc.url
				case "OPENSVC_AI_MCP_CA_FILE":
					return tc.ca
				}
				return ""
			})
			if tc.wantErr {
				if err == nil {
					t.Fatal("invalid configuration succeeded")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.URL == "" || cfg.CAFile != tc.ca {
				t.Fatalf("unexpected config: %+v", cfg)
			}
		})
	}
	if _, err := loadMCP(func(key string) string {
		if key == "OPENSVC_AI_MCP_SOCKET_PATH" {
			return "/run/mcp.sock"
		}
		return ""
	}); err == nil {
		t.Fatal("obsolete socket setting succeeded")
	}
}
