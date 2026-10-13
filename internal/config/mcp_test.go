package config

import (
	"strings"
	"testing"
)

func TestLoadOM3MCPSocket(t *testing.T) {
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
				if key == "OPENSVC_AI_OM3_MCP_SOCKET" {
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
			if cfg.OM3SocketPath != strings.TrimSpace(tc.socket) {
				t.Fatalf("unexpected config: %+v", cfg)
			}
		})
	}
}

func TestLoadOC3MCPSocketIsOptional(t *testing.T) {
	for _, tc := range []struct {
		name, socket, want string
		wantErr            bool
	}{
		{"unset", "", "", false},
		{"blank", "   ", "", false},
		{"absolute", "/run/opensvc-oc3-mcp/delegated.sock", "/run/opensvc-oc3-mcp/delegated.sock", false},
		{"trimmed", " /run/opensvc-oc3-mcp/delegated.sock ", "/run/opensvc-oc3-mcp/delegated.sock", false},
		{"relative", "run/delegated.sock", "", true},
		{"root", "/", "", true},
		{"too long", "/" + strings.Repeat("a", maxUnixSocketPathBytes), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadMCP(func(key string) string {
				switch key {
				case "OPENSVC_AI_OM3_MCP_SOCKET":
					return "/run/opensvc-mcp/delegated.sock"
				case "OPENSVC_AI_OC3_MCP_SOCKET":
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
			if err != nil || cfg.OC3SocketPath != tc.want || cfg.OM3SocketPath != "/run/opensvc-mcp/delegated.sock" {
				t.Fatalf("config=%+v err=%v", cfg, err)
			}
		})
	}
}
