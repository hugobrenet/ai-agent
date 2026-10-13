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
	request := httptest.NewRequest(http.MethodPost, "/v1/om3/conversations/conversation-1/turns", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(testBackendHeader, "cluster-id")
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
	request := httptest.NewRequest(http.MethodPost, "/v1/om3/conversations/conversation-1/turns", nil)
	request.Header.Set("Authorization", "Bearer opaque-access-token")
	request.Header.Set(testBackendHeader, "cluster-id")
	requireAccessToken(verifier, auditLogger{logger: discardAuditLogger()}, next).ServeHTTP(httptest.NewRecorder(), request)
	if !called {
		t.Fatal("protected operation did not receive the verified identity")
	}
}

type backendContextKey struct{}

// headerAuthenticator reads its backend header, as a backend does, and
// carries it in the returned context.
type headerAuthenticator struct {
	calls int
}

func (a *headerAuthenticator) Headers() []string { return []string{testBackendHeader} }

func (a *headerAuthenticator) Authenticate(ctx context.Context, header http.Header, token string) (context.Context, auth.Identity, error) {
	a.calls++
	if token != "bearer" || header.Get(testBackendHeader) != "target" {
		return nil, auth.Identity{}, auth.ErrInvalidToken
	}
	return context.WithValue(ctx, backendContextKey{}, "target"), auth.Identity{Issuer: "issuer", Subject: "subject"}, nil
}

func TestMiddlewareHandsTheBackendHeadersToItsAuthenticator(t *testing.T) {
	for _, tc := range []struct {
		name       string
		target     string
		wantStatus int
	}{
		{"accepted", "target", 204},
		{"refused", "other", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authenticator := &headerAuthenticator{}
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(testBackendHeader) != "" || r.Header.Get("Authorization") != "" || r.Context().Value(backendContextKey{}) != "target" {
					t.Fatal("backend headers were not moved to the authenticator context")
				}
				if identity, ok := auth.IdentityFromContext(r.Context()); !ok || identity.Subject != "subject" {
					t.Fatal("identity was not carried to the handler")
				}
				w.WriteHeader(204)
			})
			request := httptest.NewRequest("GET", "/v1/test/conversations", nil)
			request.Header.Set("Authorization", "Bearer bearer")
			request.Header.Set(testBackendHeader, tc.target)
			response := httptest.NewRecorder()
			requireAccessToken(authenticator, auditLogger{logger: discardAuditLogger()}, next).ServeHTTP(response, request)
			if response.Code != tc.wantStatus || authenticator.calls != 1 {
				t.Fatalf("status=%d calls=%d", response.Code, authenticator.calls)
			}
		})
	}
}
