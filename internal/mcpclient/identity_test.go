package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opensvc/ai-agent/internal/auth"
)

func TestVerifyIdentityThroughTrustedHTTPSMCP(t *testing.T) {
	t.Run("native", func(t *testing.T) { testVerifyIdentityThroughTrustedHTTPSMCP(t, false) })
	t.Run("openid", func(t *testing.T) { testVerifyIdentityThroughTrustedHTTPSMCP(t, true) })
}

func testVerifyIdentityThroughTrustedHTTPSMCP(t *testing.T, openID bool) {
	// Deliberately not a JWT: only MCP may interpret this credential.
	const token = "opaque-access-token"
	identity := auth.Identity{ClusterID: "cluster-a", Subject: "alice", Issuer: "node-a", ExpiresAt: time.Now().Add(time.Hour).UTC()}
	target := ""
	node := ""
	ctx := t.Context()
	if openID {
		target = "cluster-a"
		node = "node-b"
		ctx = auth.WithTargetCluster(ctx, target)
		ctx = auth.WithTargetNode(ctx, node)
		identity.Subject, identity.Issuer = "opaque-subject", "https://idp.example.test/"
	}
	var mode atomic.Int32
	var calls atomic.Int32
	endpoint, ca := serveHTTPS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get(auth.ClusterIDHeader) != target || r.Header.Get(auth.NodeHeader) != node {
			t.Error("whoami request lost its explicit target")
		}
		if r.URL.Path != "/mcp/auth/whoami" || r.Method != "GET" {
			t.Error("identity request lost its route")
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			if r.Header.Get("Authorization") != "Bearer malformed" {
				t.Error("identity request changed the bearer")
			}
			w.WriteHeader(401)
			return
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
			other.Subject = ""
			_ = json.NewEncoder(w).Encode(other)
			return
		case 6:
			other := identity
			other.ClusterID = ""
			_ = json.NewEncoder(w).Encode(other)
			return
		case 7:
			other := identity
			other.Issuer = ""
			_ = json.NewEncoder(w).Encode(other)
			return
		case 8:
			other := identity
			other.ExpiresAt = time.Time{}
			_ = json.NewEncoder(w).Encode(other)
			return
		case 9:
			http.Redirect(w, r, "https://foreign.invalid/sink", 302)
			return
		case 10:
			other := identity
			other.ExpiresAt = time.Now().Add(-time.Second)
			_ = json.NewEncoder(w).Encode(other)
			return
		case 11:
			w.WriteHeader(403)
			return
		case 12:
			w.Header().Set("Content-Type", "text/plain")
			_ = json.NewEncoder(w).Encode(identity)
			return
		case 13:
			other := identity
			other.Subject = "alice\nroot"
			_ = json.NewEncoder(w).Encode(other)
			return
		case 14:
			other := identity
			other.Issuer = strings.Repeat("x", 257)
			_ = json.NewEncoder(w).Encode(other)
			return
		case 15:
			other := identity
			other.ClusterID = " padded "
			_ = json.NewEncoder(w).Encode(other)
			return
		case 16:
			other := identity
			other.Subject = "invisible\u200bsubject"
			_ = json.NewEncoder(w).Encode(other)
			return
		default:
			_ = json.NewEncoder(w).Encode(identity)
		}
	}))
	client, err := New(endpoint, ca)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Verify(ctx, token)
	if err != nil || got.ClusterID != identity.ClusterID || got.Subject != identity.Subject || got.Issuer != identity.Issuer || !got.ExpiresAt.Equal(identity.ExpiresAt) {
		t.Fatalf("identity=%+v, err=%v", got, err)
	}
	for _, tc := range []struct {
		name string
		mode int32
		want error
	}{
		{"unauthorized", 1, auth.ErrInvalidToken},
		{"unavailable", 2, auth.ErrVerificationUnavailable},
		{"oversized response", 3, auth.ErrVerificationUnavailable},
		{"malformed JSON", 4, auth.ErrVerificationUnavailable},
		{"missing subject", 5, auth.ErrVerificationUnavailable},
		{"missing cluster", 6, auth.ErrVerificationUnavailable},
		{"missing issuer", 7, auth.ErrVerificationUnavailable},
		{"missing expiry", 8, auth.ErrVerificationUnavailable},
		{"redirect", 9, auth.ErrVerificationUnavailable},
		{"expired identity", 10, auth.ErrInvalidToken},
		{"forbidden", 11, auth.ErrInvalidToken},
		{"wrong content type", 12, auth.ErrVerificationUnavailable},
		{"control character", 13, auth.ErrVerificationUnavailable},
		{"oversized identity field", 14, auth.ErrVerificationUnavailable},
		{"padded identity field", 15, auth.ErrVerificationUnavailable},
		{"format character", 16, auth.ErrVerificationUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode.Store(tc.mode)
			_, err := client.Verify(ctx, token)
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), token) {
				t.Fatalf("err=%v, want=%v", err, tc.want)
			}
		})
	}
	before := calls.Load()
	if _, err := client.Verify(ctx, "malformed"); !errors.Is(err, auth.ErrInvalidToken) || calls.Load() != before+1 {
		t.Fatal("malformed credential was not delegated to MCP for refusal")
	}
	before = calls.Load()
	untrusted, err := New(endpoint, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := untrusted.Verify(ctx, token); !errors.Is(err, auth.ErrVerificationUnavailable) || calls.Load() != before {
		t.Fatal("credentials sent to untrusted TLS server")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := client.Verify(ctx, token); !errors.Is(err, auth.ErrVerificationUnavailable) || calls.Load() != before {
		t.Fatal("cancelled identity check contacted MCP")
	}
}

func TestVerifyPreservesCallerDeadline(t *testing.T) {
	entered := make(chan struct{})
	endpoint, ca := serveHTTPS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	client, err := New(endpoint, ca)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = client.Verify(ctx, "opaque-access-token")
	if !errors.Is(err, auth.ErrVerificationUnavailable) || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("err=%v, context error=%v", err, ctx.Err())
	}
	select {
	case <-entered:
	default:
		t.Fatal("identity request did not reach MCP")
	}
}
