package mcpclient

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/opensvc/ai-agent/internal/auth"
)

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var calls atomic.Int64
	socket := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing delegated token")
		}
		http.Redirect(w, r, socketOrigin+"/elsewhere", http.StatusTemporaryRedirect)
	}))
	client, err := New(socket)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(auth.WithBearerToken(t.Context(), "test-token"), http.MethodPost, client.endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || calls.Load() != 1 {
		t.Fatal("redirect followed")
	}
}

func TestClientBindsTokenToSocketOrigin(t *testing.T) {
	var calls atomic.Int64
	socket := serveUnix(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	client, err := New(socket)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ url, host string }{
		{"https://opensvc-mcp/mcp", ""},
		{"http://other.example.test/mcp", ""},
		{client.endpoint, "other.example.test"},
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
		t.Fatal("request reached MCP with an unexpected origin")
	}
}
