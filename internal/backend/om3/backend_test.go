package om3

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
	ClusterID string    `json:"cluster_id"`
	Subject   string    `json:"subject"`
	Issuer    string    `json:"issuer"`
	ExpiresAt time.Time `json:"expires_at"`
}

func serveUnix(t *testing.T, handler http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "om3")
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

// newAuthenticator serves a whoami bridge answering answer for the cluster
// and node headers it receives.
func newAuthenticator(t *testing.T, answer func(cluster, node string) whoami, calls *atomic.Int32) *Authenticator {
	t.Helper()
	socket := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/mcp/auth/whoami" || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("unexpected whoami request %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer(r.Header.Get(ClusterIDHeader), r.Header.Get(NodeHeader)))
	}))
	client, err := NewMCPClient(socket)
	if err != nil {
		t.Fatal(err)
	}
	return NewAuthenticator(client)
}

func TestAuthenticateChecksTheTargetHeadersBeforeTheMCP(t *testing.T) {
	for _, tc := range []struct {
		name            string
		clusters        []string
		nodes           []string
		verifiedCluster string
		wantCalls       int32
		wantErr         error
	}{
		{"valid", []string{"cluster-a"}, nil, "cluster-a", 1, nil},
		{"valid with node", []string{"cluster-a"}, []string{"node-b"}, "cluster-a", 1, nil},
		{"missing cluster", nil, nil, "cluster-a", 0, auth.ErrInvalidToken},
		{"empty cluster", []string{""}, nil, "cluster-a", 0, auth.ErrInvalidToken},
		{"duplicate cluster", []string{"cluster-a", "cluster-a"}, nil, "cluster-a", 0, auth.ErrInvalidToken},
		{"combined clusters", []string{"cluster-a,cluster-b"}, nil, "cluster-a", 0, auth.ErrInvalidToken},
		{"padded cluster", []string{" cluster-a "}, nil, "cluster-a", 0, auth.ErrInvalidToken},
		{"oversized cluster", []string{strings.Repeat("a", 257)}, nil, "cluster-a", 0, auth.ErrInvalidToken},
		{"control character", []string{"cluster\na"}, nil, "cluster-a", 0, auth.ErrInvalidToken},
		{"empty node", []string{"cluster-a"}, []string{""}, "cluster-a", 0, auth.ErrInvalidToken},
		{"duplicate node", []string{"cluster-a"}, []string{"node-b", "node-b"}, "cluster-a", 0, auth.ErrInvalidToken},
		{"combined nodes", []string{"cluster-a"}, []string{"node-a,node-b"}, "cluster-a", 0, auth.ErrInvalidToken},
		{"padded node", []string{"cluster-a"}, []string{" node-b "}, "cluster-a", 0, auth.ErrInvalidToken},
		{"wrong verified cluster", []string{"cluster-a"}, nil, "cluster-b", 1, auth.ErrInvalidToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			authenticator := newAuthenticator(t, func(cluster, node string) whoami {
				if cluster != "cluster-a" || len(tc.nodes) == 1 && node != tc.nodes[0] || len(tc.nodes) == 0 && node != "" {
					t.Errorf("whoami received cluster=%q node=%q", cluster, node)
				}
				return whoami{ClusterID: tc.verifiedCluster, Subject: "alice", Issuer: "issuer", ExpiresAt: time.Now().Add(time.Hour)}
			}, &calls)
			header := http.Header{}
			for _, value := range tc.clusters {
				header.Add(ClusterIDHeader, value)
			}
			for _, value := range tc.nodes {
				header.Add(NodeHeader, value)
			}
			ctx, identity, err := authenticator.Authenticate(t.Context(), header, "token")
			if !errors.Is(err, tc.wantErr) || calls.Load() != tc.wantCalls {
				t.Fatalf("err=%v calls=%d", err, calls.Load())
			}
			if tc.wantErr != nil {
				return
			}
			headers := auth.MCPHeadersFromContext(ctx)
			if identity.ClusterID != "cluster-a" || identity.Subject != "alice" || headers.Get(ClusterIDHeader) != "cluster-a" || len(tc.nodes) == 1 && headers.Get(NodeHeader) != "node-b" {
				t.Fatalf("identity=%+v headers=%v", identity, headers)
			}
		})
	}
}

func TestAuthenticateValidatesTheWhoAmIAnswer(t *testing.T) {
	valid := whoami{ClusterID: "cluster-a", Subject: "opaque-subject", Issuer: "https://idp.example.test/", ExpiresAt: time.Now().Add(time.Hour).UTC()}
	for _, tc := range []struct {
		name   string
		change func(*whoami)
		want   error
	}{
		{"missing subject", func(w *whoami) { w.Subject = "" }, auth.ErrVerificationUnavailable},
		{"missing cluster", func(w *whoami) { w.ClusterID = "" }, auth.ErrVerificationUnavailable},
		{"missing issuer", func(w *whoami) { w.Issuer = "" }, auth.ErrVerificationUnavailable},
		{"missing expiry", func(w *whoami) { w.ExpiresAt = time.Time{} }, auth.ErrVerificationUnavailable},
		{"expired identity", func(w *whoami) { w.ExpiresAt = time.Now().Add(-time.Second) }, auth.ErrInvalidToken},
		{"control character", func(w *whoami) { w.Subject = "alice\nroot" }, auth.ErrVerificationUnavailable},
		{"oversized identity field", func(w *whoami) { w.Issuer = strings.Repeat("x", 257) }, auth.ErrVerificationUnavailable},
		{"padded identity field", func(w *whoami) { w.ClusterID = " padded " }, auth.ErrVerificationUnavailable},
		{"format character", func(w *whoami) { w.Subject = "invisible\u200bsubject" }, auth.ErrVerificationUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			authenticator := newAuthenticator(t, func(string, string) whoami {
				answer := valid
				tc.change(&answer)
				return answer
			}, &calls)
			if _, _, err := authenticator.Authenticate(t.Context(), clusterHeader("cluster-a"), "token"); !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want=%v", err, tc.want)
			}
		})
	}
	var calls atomic.Int32
	authenticator := newAuthenticator(t, func(string, string) whoami { return valid }, &calls)
	_, identity, err := authenticator.Authenticate(t.Context(), clusterHeader("cluster-a"), "token")
	if err != nil || identity.Subject != valid.Subject || identity.Issuer != valid.Issuer || !identity.ExpiresAt.Equal(valid.ExpiresAt) {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
}

func TestHeadersNameTheTargetHeaders(t *testing.T) {
	if got := NewAuthenticator(nil).Headers(); len(got) != 2 || got[0] != ClusterIDHeader || got[1] != NodeHeader {
		t.Fatalf("headers = %v", got)
	}
}

func clusterHeader(cluster string) http.Header {
	header := http.Header{}
	header.Set(ClusterIDHeader, cluster)
	return header
}
