package oc3

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opensvc/ai-agent/internal/auth"
)

type whoami struct {
	Issuer    string    `json:"issuer"`
	Subject   string    `json:"subject"`
	ExpiresAt time.Time `json:"expires_at"`
}

func serveUnix(t *testing.T, handler http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "oc3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "mcp.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return path
}

// newAuthenticator serves a whoami bridge answering status and answer, and
// checks that the request carries the token and no other header of the agent.
func newAuthenticator(t *testing.T, status int, answer whoami, calls *atomic.Int32) *Authenticator {
	t.Helper()
	socket := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/mcp/auth/whoami" || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("unexpected whoami request %s", r.URL.Path)
		}
		for name := range r.Header {
			if strings.HasPrefix(http.CanonicalHeaderKey(name), "X-Opensvc-") {
				t.Errorf("whoami request carries %s", name)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(answer)
	}))
	client, err := NewMCPClient(socket)
	if err != nil {
		t.Fatal(err)
	}
	return NewAuthenticator(client)
}

func TestAuthenticateTakesTheIdentityTheMCPConfirms(t *testing.T) {
	valid := whoami{Issuer: "https://sso.example.test/application/o/collector/", Subject: "opaque-subject", ExpiresAt: time.Now().Add(time.Hour).UTC()}
	var calls atomic.Int32
	authenticator := newAuthenticator(t, http.StatusOK, valid, &calls)
	// A header of another backend is neither read nor forwarded.
	header := http.Header{}
	header.Set("X-OpenSVC-Cluster-ID", "cluster-a")
	ctx, identity, err := authenticator.Authenticate(t.Context(), header, "token")
	if err != nil || ctx == nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	if identity.ClusterID != "" || identity.Issuer != valid.Issuer || identity.Subject != valid.Subject || !identity.ExpiresAt.Equal(valid.ExpiresAt) {
		t.Fatalf("identity = %+v", identity)
	}
	if auth.MCPHeadersFromContext(ctx) != nil {
		t.Fatal("the oc3 backend carries MCP headers")
	}
}

func TestAuthenticateValidatesTheWhoAmIAnswer(t *testing.T) {
	valid := whoami{Issuer: "https://sso.example.test/application/o/collector/", Subject: "opaque-subject", ExpiresAt: time.Now().Add(time.Hour).UTC()}
	for _, tc := range []struct {
		name   string
		status int
		change func(*whoami)
		want   error
	}{
		{"refused by the collector", http.StatusUnauthorized, func(*whoami) {}, auth.ErrInvalidToken},
		{"forbidden by the collector", http.StatusForbidden, func(*whoami) {}, auth.ErrInvalidToken},
		{"collector unavailable", http.StatusBadGateway, func(*whoami) {}, auth.ErrVerificationUnavailable},
		{"missing issuer", http.StatusOK, func(w *whoami) { w.Issuer = "" }, auth.ErrVerificationUnavailable},
		{"missing subject", http.StatusOK, func(w *whoami) { w.Subject = "" }, auth.ErrVerificationUnavailable},
		{"missing expiry", http.StatusOK, func(w *whoami) { w.ExpiresAt = time.Time{} }, auth.ErrVerificationUnavailable},
		{"expired identity", http.StatusOK, func(w *whoami) { w.ExpiresAt = time.Now().Add(-time.Second) }, auth.ErrInvalidToken},
		{"control character", http.StatusOK, func(w *whoami) { w.Subject = "alice\nroot" }, auth.ErrVerificationUnavailable},
		{"oversized identity field", http.StatusOK, func(w *whoami) { w.Issuer = strings.Repeat("x", 257) }, auth.ErrVerificationUnavailable},
		{"padded identity field", http.StatusOK, func(w *whoami) { w.Subject = " padded " }, auth.ErrVerificationUnavailable},
		{"format character", http.StatusOK, func(w *whoami) { w.Subject = "invisible\u200bsubject" }, auth.ErrVerificationUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := valid
			tc.change(&answer)
			var calls atomic.Int32
			authenticator := newAuthenticator(t, tc.status, answer, &calls)
			if _, _, err := authenticator.Authenticate(t.Context(), http.Header{}, "token"); !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want=%v", err, tc.want)
			}
		})
	}
}

func TestHeadersIsEmpty(t *testing.T) {
	if got := NewAuthenticator(nil).Headers(); len(got) != 0 {
		t.Fatalf("headers = %v", got)
	}
}
