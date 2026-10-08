package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/opensvc/ai-agent/internal/agent"
	"github.com/opensvc/ai-agent/internal/auth"
	"github.com/opensvc/ai-agent/internal/conversation"
	conversationsqlite "github.com/opensvc/ai-agent/internal/conversation/sqlite"
	"github.com/opensvc/ai-agent/internal/llm"
	"github.com/opensvc/ai-agent/internal/mcpclient"
)

func TestRemoteIdentityProtectsLocalConversationsAndModelCalls(t *testing.T) {
	t.Run("native", func(t *testing.T) { testRemoteIdentityProtectsLocalConversationsAndModelCalls(t, false) })
	t.Run("openid", func(t *testing.T) { testRemoteIdentityProtectsLocalConversationsAndModelCalls(t, true) })
}

func testRemoteIdentityProtectsLocalConversationsAndModelCalls(t *testing.T, openID bool) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	targets := make(map[string]string)
	signWithExpiry := func(cluster, user string, key *rsa.PrivateKey, expiresAt time.Time) string {
		claims := jwt.MapClaims{"cluster_id": cluster, "iss": "node-a", "sub": user, "exp": expiresAt.Unix(), "token_use": "access"}
		if openID {
			delete(claims, "cluster_id")
			delete(claims, "token_use")
			claims["iss"] = "https://idp.example.test/"
			claims["sub"] = "opaque-" + user
			claims["preferred_username"] = user
			claims["aud"] = "client-" + cluster
		}
		unsigned := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		unsigned.Header["kid"] = "key"
		raw, err := unsigned.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		targets[raw] = cluster
		return raw
	}
	sign := func(cluster, user string, key *rsa.PrivateKey) string {
		return signWithExpiry(cluster, user, key, time.Now().Add(time.Hour))
	}
	alice := sign("cluster-a", "alice", key)
	bob := sign("cluster-a", "bob", key)
	otherCluster := sign("cluster-b", "alice", key)
	var unavailable atomic.Bool
	var checks atomic.Int32
	mcpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checks.Add(1)
		if unavailable.Load() {
			w.WriteHeader(502)
			return
		}
		if r.URL.Path != "/mcp/auth/whoami" {
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		claims := jwt.MapClaims{}
		target := r.Header.Get(auth.ClusterIDHeader)
		options := []jwt.ParserOption{jwt.WithValidMethods([]string{"RS256"}), jwt.WithExpirationRequired()}
		if openID {
			if (target != "cluster-a" && target != "cluster-b") || r.Header.Get(auth.NodeHeader) != "node-b" {
				w.WriteHeader(401)
				return
			}
			options = append(options, jwt.WithIssuer("https://idp.example.test/"), jwt.WithAudience("client-"+target))
		}
		if _, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return &key.PublicKey, nil }, options...); err != nil {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if !openID {
			target = claims["cluster_id"].(string)
		}
		_ = json.NewEncoder(w).Encode(auth.Identity{ClusterID: target, Issuer: claims["iss"].(string), Subject: claims["sub"].(string), ExpiresAt: time.Unix(int64(claims["exp"].(float64)), 0)})
	}))
	t.Cleanup(mcpServer.Close)
	caFile := filepath.Join(t.TempDir(), "mcp-ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: mcpServer.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	verifier, err := mcpclient.New(mcpServer.URL+"/mcp", caFile)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := conversationsqlite.Open(t.Context(), conversationsqlite.Config{Path: filepath.Join(directory, "conversations.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var modelCalls atomic.Int32
	runner := apiTurnRunnerFunc(func(_ context.Context, _ []llm.Message, prompt string, emit agent.EmitFunc) (agent.TurnResult, error) {
		modelCalls.Add(1)
		return agent.TurnResult{Messages: []llm.Message{{Role: llm.RoleUser, Text: prompt}, {Role: llm.RoleAssistant, Text: "answer"}}, FinishReason: llm.FinishReasonCompleted}, nil
	})
	service, err := conversation.NewService(store, runner, conversation.ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(askerFunc(func(context.Context, string, agent.EmitFunc) error { modelCalls.Add(1); return nil }), service, verifier, HandlerConfig{MaxConcurrentAsks: 4, AuditLogger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		request := requestWithToken(method, path, token, body)
		if openID {
			request.Header.Set(auth.ClusterIDHeader, targets[token])
			request.Header.Set(auth.NodeHeader, "node-b")
		}
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	created := call("POST", "/v1/conversations", alice, "")
	if created.Code != 201 {
		t.Fatalf("create: %d", created.Code)
	}
	var item ConversationEnvelope
	if err := json.Unmarshal(created.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	path := "/v1/conversations/" + item.Conversation.ID
	for _, token := range []string{bob, otherCluster} {
		for _, method := range []string{"GET", "PATCH", "DELETE"} {
			body := ""
			if method == "PATCH" {
				body = `{"title":"foreign"}`
			}
			if response := call(method, path, token, body); response.Code != 404 {
				t.Fatalf("foreign access method=%s status=%d", method, response.Code)
			}
		}
		if response := call("POST", path+"/turns", token, `{"prompt":"read history"}`); response.Code != 404 {
			t.Fatalf("foreign turn: %d", response.Code)
		}
		response := call("GET", "/v1/conversations", token, "")
		if response.Code != 200 || strings.Contains(response.Body.String(), item.Conversation.ID) {
			t.Fatal("foreign conversation leaked")
		}
	}
	foreignKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	forged := sign("cluster-a", "alice", foreignKey)
	expired := signWithExpiry("cluster-a", "alice", key, time.Now().Add(-time.Second))
	targets["malformed"] = "cluster-a"
	for _, token := range []string{forged, expired, "malformed"} {
		for _, tc := range []struct{ method, path, body string }{
			{"GET", "/v1/conversations", ""}, {"GET", path, ""}, {"GET", path + "/messages", ""}, {"DELETE", path, ""},
			{"PATCH", path, `{"title":"refused"}`}, {"POST", path + "/turns", `{"prompt":"refused"}`},
			{"POST", "/v1/ask", `{"prompt":"refused"}`}, {"POST", "/v1/conversations", ""},
		} {
			before := checks.Load()
			response := call(tc.method, tc.path, token, tc.body)
			if response.Code != 401 || strings.Contains(response.Body.String(), token) {
				t.Fatalf("refused credential admitted: status=%d", response.Code)
			}
			if checks.Load() != before+1 {
				t.Fatal("agent did not delegate credential validation to MCP")
			}
		}
	}
	if modelCalls.Load() != 0 {
		t.Fatal("model invoked before authentication/ownership check")
	}
	unavailable.Store(true)
	if response := call("DELETE", path, alice, ""); response.Code != 503 {
		t.Fatalf("unavailable verifier bypassed: %d", response.Code)
	}
	unavailable.Store(false)
	if response := call("GET", path, alice, ""); response.Code != 200 {
		t.Fatal("refused deletion changed existing conversation")
	}
	if response := call("POST", path+"/turns", alice, `{"prompt":"hello"}`); response.Code != 200 || modelCalls.Load() != 1 {
		t.Fatalf("authorized turn rejected: %d", response.Code)
	}
	if checks.Load() < 16 {
		t.Fatal("identity was not checked independently for each API operation")
	}
}
