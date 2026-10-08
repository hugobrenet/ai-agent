package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/opensvc/ai-agent/internal/auth"
)

func TestRequireAccessTokenRemovesAuthorizationHeaderAndPreservesContext(t *testing.T) {
	const token = "delegated-token"
	verifier := tokenVerifierFunc(func(_ context.Context, got string) (auth.Identity, error) {
		if got != token {
			t.Fatalf("verifier token = %q", got)
		}
		return auth.Identity{ClusterID: "cluster-id", Subject: "alice", Issuer: "node-a"}, nil
	})
	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		called = true
		if got := request.Header.Get("Authorization"); got != "" {
			t.Fatalf("downstream Authorization header = %q", got)
		}
		if got, ok := auth.BearerTokenFromContext(request.Context()); !ok || got != token {
			t.Fatalf("delegated context token = %q, %v", got, ok)
		}
		if identity, ok := auth.IdentityFromContext(request.Context()); !ok || identity.Subject != "alice" {
			t.Fatalf("verified identity = %+v, %v", identity, ok)
		}
	})
	handler := requireAccessToken(verifier, auditLogger{logger: discardAuditLogger()}, next)
	request := httptest.NewRequest(http.MethodPost, "/v1/ask", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if !called {
		t.Fatal("downstream handler was not called")
	}
}

func TestRequireAccessTokenUsesVerifiedIdentityExpiry(t *testing.T) {
	expiresAt := time.Now().Add(100 * time.Millisecond)
	verifier := tokenVerifierFunc(func(context.Context, string) (auth.Identity, error) {
		return auth.Identity{ClusterID: "cluster-id", Subject: "alice", Issuer: "node-a", ExpiresAt: expiresAt}, nil
	})
	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		called = true
		deadline, ok := request.Context().Deadline()
		if !ok || !deadline.Equal(expiresAt) {
			t.Fatalf("deadline=%v, want verified expiry=%v", deadline, expiresAt)
		}
		<-request.Context().Done()
		if !errors.Is(request.Context().Err(), context.DeadlineExceeded) {
			t.Fatal("request was not cancelled at verified expiry")
		}
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/ask", nil)
	request.Header.Set("Authorization", "Bearer opaque-access-token")
	requireAccessToken(verifier, auditLogger{logger: discardAuditLogger()}, next).ServeHTTP(httptest.NewRecorder(), request)
	if !called {
		t.Fatal("protected operation did not receive the verified identity")
	}
}
