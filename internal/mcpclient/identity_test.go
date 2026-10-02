package mcpclient

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hugobrenet/opensvc-ai-agent/internal/auth"
)

func identityToken(t testing.TB, key *rsa.PrivateKey, cluster, user string) string {
	t.Helper()
	raw, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"cluster_id": cluster, "iss": "node-a", "sub": user, "exp": time.Now().Add(time.Hour).Unix(), "token_use": "access"}).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestVerifyIdentityThroughTrustedHTTPSMCP(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	token := identityToken(t, key, "cluster-a", "alice")
	delegation, err := auth.CheckDelegation(token)
	if err != nil {
		t.Fatal(err)
	}
	identity := auth.Identity{ClusterID: delegation.ClusterID, Subject: delegation.Subject, Issuer: delegation.Issuer, ExpiresAt: delegation.ExpiresAt}
	var mode atomic.Int32
	var calls atomic.Int32
	endpoint, ca := serveHTTPS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/mcp/auth/whoami" || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("identity request lost its route or exact JWT")
		}
		w.Header().Set("Content-Type", "application/json")
		switch mode.Load() {
		case 1:
			w.WriteHeader(401)
			return
		case 2:
			w.WriteHeader(503)
			return
		case 3:
			_, _ = w.Write([]byte(strings.Repeat("x", maxIdentityResponseBytes+1)))
			return
		case 4:
			_, _ = w.Write([]byte(token))
			return
		case 5:
			other := identity
			other.Subject = "bob"
			_ = json.NewEncoder(w).Encode(other)
			return
		case 6:
			other := identity
			other.ClusterID = "cluster-b"
			_ = json.NewEncoder(w).Encode(other)
			return
		case 7:
			other := identity
			other.Issuer = "node-b"
			_ = json.NewEncoder(w).Encode(other)
			return
		case 8:
			other := identity
			other.ExpiresAt = other.ExpiresAt.Add(time.Second)
			_ = json.NewEncoder(w).Encode(other)
			return
		case 9:
			http.Redirect(w, r, "https://foreign.invalid/sink", 302)
			return
		default:
			_ = json.NewEncoder(w).Encode(identity)
		}
	}))
	client, err := New(endpoint, ca)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Verify(t.Context(), token)
	if err != nil || got.ClusterID != identity.ClusterID || got.Subject != identity.Subject || got.Issuer != identity.Issuer {
		t.Fatalf("identity=%+v, err=%v", got, err)
	}
	for modeValue, want := range map[int32]error{1: auth.ErrInvalidToken, 2: auth.ErrVerificationUnavailable, 3: auth.ErrVerificationUnavailable, 4: auth.ErrVerificationUnavailable, 5: auth.ErrInvalidToken, 6: auth.ErrInvalidToken, 7: auth.ErrInvalidToken, 8: auth.ErrInvalidToken, 9: auth.ErrVerificationUnavailable} {
		mode.Store(modeValue)
		if _, err := client.Verify(t.Context(), token); !errors.Is(err, want) || strings.Contains(err.Error(), token) {
			t.Fatalf("mode=%d err=%v", modeValue, err)
		}
	}
	before := calls.Load()
	if _, err := client.Verify(t.Context(), "malformed"); !errors.Is(err, auth.ErrInvalidToken) || calls.Load() != before {
		t.Fatal("invalid token contacted MCP")
	}
	untrusted, err := New(endpoint, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := untrusted.Verify(t.Context(), token); !errors.Is(err, auth.ErrVerificationUnavailable) || calls.Load() != before {
		t.Fatal("credentials sent to untrusted TLS server")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Verify(ctx, token); err == nil {
		t.Fatal("cancelled identity check accepted")
	}
}
