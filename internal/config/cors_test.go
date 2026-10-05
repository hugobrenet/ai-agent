package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestLoadCORSOrigins(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  []string
	}{
		{"", nil}, {"  ", nil}, {" * ", []string{"*"}},
		{"https://webapp-a.example:1215, https://webapp-b.example:1215", []string{"https://webapp-a.example:1215", "https://webapp-b.example:1215"}},
		{"https://WEBAPP.example:443,https://webapp.example", []string{"https://webapp.example"}},
		{"http://localhost:5173,http://localhost:80", []string{"http://localhost:5173", "http://localhost"}},
		{"https://[2001:db8::1]:1215", []string{"https://[2001:db8::1]:1215"}},
	} {
		cfg, err := load(processEnv(map[string]string{"OPENSVC_AI_CORS_ALLOWED_ORIGINS": tc.value}))
		if err != nil || !reflect.DeepEqual(cfg.CORSAllowedOrigins, tc.want) {
			t.Fatalf("origins=%q result=%v err=%v", tc.value, cfg.CORSAllowedOrigins, err)
		}
	}
}

func TestLoadRejectsInvalidCORSOriginsWithoutEchoingInput(t *testing.T) {
	for _, value := range []string{
		"*,https://webapp.example", "https://*.example", "null", "webapp.example", "ftp://webapp.example", "https://",
		"https://user:never-echo-this-value@webapp.example", "https://webapp.example/", "https://webapp.example/ui",
		"https://webapp.example?q=never-echo-this-value", "https://webapp.example?", "https://webapp.example#fragment", "https://webapp.example#",
		"https://webapp.example,", ",https://webapp.example", "https://webapp.example,,https://other.example",
		"https://webapp.example:0", "https://webapp.example:65536", "https://webapp.example:", "https://webapp.example:port",
		"https://web app.example", "https://webapp.example\n.evil", "https://webapp.example\\evil", "https://[not-an-ip]",
		strings.Repeat("x", (64<<10)+1),
	} {
		_, err := load(processEnv(map[string]string{"OPENSVC_AI_CORS_ALLOWED_ORIGINS": value}))
		if err == nil || !strings.Contains(err.Error(), "OPENSVC_AI_CORS_ALLOWED_ORIGINS") || strings.Contains(err.Error(), "never-echo-this-value") {
			t.Fatalf("invalid origin accepted or input leaked: %v", err)
		}
	}
}
