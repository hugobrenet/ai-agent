package config

import (
	"strings"
	"testing"
)

func TestLoadMCPSocket(t *testing.T) {
	for _, tc := range []struct {
		name, socket string
		wantErr      bool
	}{
		{"missing", "", true},
		{"absolute", "/run/opensvc-mcp/delegated.sock", false},
		{"trimmed", " /run/opensvc-mcp/delegated.sock ", false},
		{"relative", "run/delegated.sock", true},
		{"root", "/", true},
		{"too long", "/" + strings.Repeat("a", maxUnixSocketPathBytes), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadMCP(func(key string) string {
				if key == "OPENSVC_AI_MCP_SOCKET" {
					return tc.socket
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
			if cfg.SocketPath != strings.TrimSpace(tc.socket) {
				t.Fatalf("unexpected config: %+v", cfg)
			}
		})
	}
}
