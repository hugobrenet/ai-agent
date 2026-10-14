package mcpclient

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opensvc/ai-agent/internal/auth"
)

func TestWhoAmIDecodesTheMCPAnswer(t *testing.T) {
	// Deliberately not a JWT: only MCP may interpret this credential.
	const token = "opaque-access-token"
	type identity struct {
		Subject string `json:"subject"`
	}
	var mode atomic.Int32
	var calls atomic.Int32
	endpoint := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get(testClusterHeader) != "cluster-a" || r.Header.Get(testNodeHeader) != "node-b" {
			t.Error("whoami request lost its headers")
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
		case 2:
			w.WriteHeader(503)
		case 3:
			_, _ = w.Write([]byte(strings.Repeat("x", maxIdentityResponseBytes+1)))
		case 4:
			_, _ = w.Write([]byte(token))
		case 5:
			http.Redirect(w, r, "https://foreign.invalid/sink", http.StatusFound)
		case 6:
			w.WriteHeader(403)
		case 7:
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(`{"subject":"alice"}`))
		default:
			_, _ = w.Write([]byte(`{"subject":"alice"}`))
		}
	}))
	client, err := New(endpoint, testClusterHeader, testNodeHeader)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithMCPHeaders(t.Context(), targetHeaders("cluster-a", "node-b"))
	var got identity
	if err := client.WhoAmI(ctx, token, &got); err != nil || got.Subject != "alice" {
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
		{"redirect", 5, auth.ErrVerificationUnavailable},
		{"forbidden", 6, auth.ErrInvalidToken},
		{"wrong content type", 7, auth.ErrVerificationUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode.Store(tc.mode)
			// A new client: a remembered refusal must not hide the next case.
			client, err := New(endpoint, testClusterHeader, testNodeHeader)
			if err != nil {
				t.Fatal(err)
			}
			err = client.WhoAmI(ctx, token, &identity{})
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), token) {
				t.Fatalf("err=%v, want=%v", err, tc.want)
			}
		})
	}
	before := calls.Load()
	if err := client.WhoAmI(ctx, "malformed", &identity{}); !errors.Is(err, auth.ErrInvalidToken) || calls.Load() != before+1 {
		t.Fatal("malformed credential was not delegated to MCP for refusal")
	}
	before = calls.Load()
	missing, err := New(filepath.Join(filepath.Dir(endpoint), "missing.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := missing.WhoAmI(ctx, token, &identity{}); !errors.Is(err, auth.ErrVerificationUnavailable) || calls.Load() != before {
		t.Fatal("unavailable MCP socket was not reported as unavailable")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := client.WhoAmI(ctx, token, &identity{}); !errors.Is(err, auth.ErrVerificationUnavailable) || calls.Load() != before {
		t.Fatal("cancelled identity check contacted MCP")
	}
}

func TestWhoAmIPreservesCallerDeadline(t *testing.T) {
	entered := make(chan struct{})
	endpoint := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	client, err := New(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err = client.WhoAmI(ctx, "opaque-access-token", &struct{}{})
	if !errors.Is(err, auth.ErrVerificationUnavailable) || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("err=%v, context error=%v", err, ctx.Err())
	}
	select {
	case <-entered:
	default:
		t.Fatal("identity request did not reach MCP")
	}
}
