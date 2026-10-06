package mcpclient

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opensvc/ai-agent/internal/auth"
)

func TestClientRejectsUntrustedTLS(t *testing.T) {
	var calls atomic.Int64
	endpoint, _ := serveHTTPS(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	client, err := New(endpoint, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Connect(auth.WithBearerToken(t.Context(), "test-token"))
	if err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("JWT sent to untrusted TLS server")
	}
}

func TestClientVerifiesTLSHostname(t *testing.T) {
	var calls atomic.Int64
	endpoint, caFile := serveHTTPS(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	client, err := New(strings.Replace(endpoint, "127.0.0.1", "localhost", 1), caFile)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Connect(auth.WithBearerToken(t.Context(), "test-token"))
	if err == nil {
		t.Fatal("incorrect certificate hostname accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("JWT sent to server with incorrect TLS hostname")
	}
}

func TestClientRejectsInvalidCAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ca.pem")
	if _, err := New("https://mcp.example.test/mcp", file); err == nil {
		t.Fatal("missing CA accepted")
	}
	for _, content := range []string{"not a certificate", strings.Repeat("x", (1<<20)+1)} {
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := New("https://mcp.example.test/mcp", file); err == nil {
			t.Fatal("invalid CA accepted")
		}
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var targetCalls atomic.Int64
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	endpoint, caFile := serveHTTPS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing delegated token")
		}
		http.Redirect(w, r, target.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	client, err := New(endpoint, caFile)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(auth.WithBearerToken(t.Context(), "test-token"), http.MethodPost, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || targetCalls.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestClientBindsTokenToConfiguredOrigin(t *testing.T) {
	var calls atomic.Int64
	endpoint, caFile := serveHTTPS(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	client, err := New(endpoint, caFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ url, host string }{
		{"http://" + strings.TrimPrefix(endpoint, "https://"), ""},
		{"https://other.example.test/mcp", ""},
		{endpoint, "other.example.test"},
	} {
		request, err := http.NewRequestWithContext(auth.WithBearerToken(t.Context(), "test-token"), http.MethodPost, tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = tc.host
		if _, err := client.httpClient.Do(request); err == nil {
			t.Fatal("foreign origin accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("request reached server with unauthorized origin")
	}
}
