package mcpclient

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hugobrenet/opensvc-ai-agent/internal/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestClientListsAndCallsToolsWithDelegatedJWT(t *testing.T) {
	t.Run("native", func(t *testing.T) { testClientListsAndCallsToolsWithDelegatedJWT(t, "") })
	t.Run("explicit cluster", func(t *testing.T) { testClientListsAndCallsToolsWithDelegatedJWT(t, "cluster-a") })
}

func testClientListsAndCallsToolsWithDelegatedJWT(t *testing.T, clusterID string) {
	const token = "delegated-test-token"

	server := mcp.NewServer(&mcp.Implementation{Name: "test-mcp", Version: "v0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "cluster_health", Description: "Get cluster health"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, map[string]string, error) {
			return nil, map[string]string{"status": "healthy"}, nil
		})

	var requestCount atomic.Int64
	streamHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	endpoint, caFile := serveHTTPS(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		if request.Header.Get(auth.ClusterIDHeader) != clusterID {
			t.Error("MCP request lost its explicit target")
			response.WriteHeader(401)
			return
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+token {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		streamHandler.ServeHTTP(response, request)
	}))

	client, err := New(endpoint, caFile)
	if err != nil {
		t.Fatalf("create MCP client: %v", err)
	}
	ctx := auth.WithBearerToken(t.Context(), token)
	ctx = auth.WithTargetCluster(ctx, clusterID)
	session, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}

	tools, err := session.ListTools(ctx)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "cluster_health" {
		t.Fatalf("got tools %#v, want cluster_health", tools)
	}

	result, err := session.CallTool(ctx, "cluster_health", map[string]any{})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["status"] != "healthy" {
		t.Fatalf("got structured result %#v, want healthy", result.StructuredContent)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	if requestCount.Load() < 4 {
		t.Errorf("got %d authenticated requests, want initialize, initialized, list, call and close", requestCount.Load())
	}
}

func TestClientRejectsMissingDelegatedJWT(t *testing.T) {
	var requestCount atomic.Int64
	endpoint, caFile := serveHTTPS(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requestCount.Add(1)
	}))

	client, err := New(endpoint, caFile)
	if err != nil {
		t.Fatalf("create MCP client: %v", err)
	}
	if _, err := client.Connect(t.Context()); err == nil || !strings.Contains(err.Error(), "JWT is missing") {
		t.Fatalf("connect error = %v, want missing JWT", err)
	}
	if requestCount.Load() != 0 {
		t.Fatalf("server received %d requests without a JWT", requestCount.Load())
	}
}

func TestClientTargetHeadersAreRequestScoped(t *testing.T) {
	wantCluster := ""
	endpoint, caFile := serveHTTPS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(auth.ClusterIDHeader) != wantCluster || r.Header.Get("Authorization") != "Bearer token" {
			t.Error("request used stale or caller-overridden headers")
		}
		w.WriteHeader(204)
	}))
	client, err := New(endpoint, caFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"cluster-a", "cluster-b", ""} {
		wantCluster = target
		ctx := auth.WithTargetCluster(auth.WithBearerToken(t.Context(), "token"), target)
		r, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set(auth.ClusterIDHeader, "wrong-cluster")
		response, err := client.httpClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if r.Header.Get(auth.ClusterIDHeader) != "wrong-cluster" {
			t.Fatal("original request was mutated")
		}
	}
}

func TestClientDoesNotExposeJWTInErrors(t *testing.T) {
	const token = "secret-jwt-value"
	endpoint, caFile := serveHTTPS(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
	}))

	client, err := New(endpoint, caFile)
	if err != nil {
		t.Fatalf("create MCP client: %v", err)
	}
	_, err = client.Connect(auth.WithBearerToken(t.Context(), token))
	if err == nil {
		t.Fatal("connect succeeded, want authentication error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error exposes JWT: %q", err)
	}
}

func TestSessionRequiresJWTOnEveryOperation(t *testing.T) {
	const token = "delegated-test-token"
	server := mcp.NewServer(&mcp.Implementation{Name: "test-mcp", Version: "v0.1.0"}, nil)
	streamHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	endpoint, caFile := serveHTTPS(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		streamHandler.ServeHTTP(response, request)
	}))

	client, err := New(endpoint, caFile)
	if err != nil {
		t.Fatalf("create MCP client: %v", err)
	}
	ctx := auth.WithBearerToken(t.Context(), token)
	session, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	_, err = session.ListTools(context.Background())
	if err == nil || !strings.Contains(err.Error(), "JWT is missing") {
		t.Fatalf("list tools error = %v, want missing JWT", err)
	}
}

func TestClientRejectsTooManyTools(t *testing.T) {
	const token = "delegated-test-token"
	server := mcp.NewServer(&mcp.Implementation{Name: "test-mcp", Version: "v0.1.0"}, nil)
	for index := 0; index <= maxMCPToolCount; index++ {
		mcp.AddTool(server, &mcp.Tool{Name: fmt.Sprintf("tool_%03d", index)},
			func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
				return nil, map[string]any{}, nil
			})
	}
	streamHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	endpoint, caFile := serveHTTPS(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		streamHandler.ServeHTTP(response, request)
	}))

	client, err := New(endpoint, caFile)
	if err != nil {
		t.Fatalf("create MCP client: %v", err)
	}
	ctx := auth.WithBearerToken(t.Context(), token)
	session, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	tools, err := session.ListTools(ctx)
	if err == nil || !strings.Contains(err.Error(), "tool count exceeds") {
		t.Fatalf("ListTools() tools=%d error=%v, want count limit", len(tools), err)
	}
	if tools != nil {
		t.Fatalf("ListTools() returned a partial catalog of %d tools", len(tools))
	}
}

func TestCallToolRejectsEmptyName(t *testing.T) {
	session := &Session{}
	if _, err := session.CallTool(t.Context(), "", nil); err == nil {
		t.Fatal("empty tool name succeeded")
	}
}

func TestNewValidatesHTTPSURL(t *testing.T) {
	for _, endpoint := range []string{"", "mcp.sock", "http://localhost/mcp", "https://user:pass@example.test/mcp", "https://example.test/", "https://example.test/mcp?q=token", "https://example.test/mcp#secret", "https://example.test:0/mcp"} {
		t.Run(endpoint, func(t *testing.T) {
			if _, err := New(endpoint, ""); err == nil {
				t.Fatal("invalid HTTPS URL succeeded")
			}
		})
	}
}

func serveHTTPS(t *testing.T, handler http.Handler) (string, string) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	caFile := filepath.Join(t.TempDir(), "mcp-ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	return server.URL + "/mcp", caFile
}
