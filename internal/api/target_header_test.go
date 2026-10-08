package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hugobrenet/opensvc-ai-agent/internal/auth"
)

func TestTargetClusterHeaderIsCheckedBeforeProtectedOperation(t *testing.T) {
	for _, tc := range []struct {
		name            string
		values          []string
		verifiedCluster string
		wantCalls       int
		wantStatus      int
	}{
		{"valid", []string{"cluster-a"}, "cluster-a", 1, 204},
		{"missing", nil, "cluster-a", 0, 401},
		{"empty", []string{""}, "cluster-a", 0, 401},
		{"duplicate", []string{"cluster-a", "cluster-a"}, "cluster-a", 0, 401},
		{"combined", []string{"cluster-a,cluster-b"}, "cluster-a", 0, 401},
		{"padded", []string{" cluster-a "}, "cluster-a", 0, 401},
		{"oversized", []string{strings.Repeat("a", 257)}, "cluster-a", 0, 401},
		{"control character", []string{"cluster\na"}, "cluster-a", 0, 401},
		{"wrong verified cluster", []string{"cluster-a"}, "cluster-b", 1, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			verifier := tokenVerifierFunc(func(ctx context.Context, token string) (auth.Identity, error) {
				calls++
				if token != "bearer" || auth.TargetClusterFromContext(ctx) != "cluster-a" {
					t.Fatal("verifier lost request headers")
				}
				return auth.Identity{ClusterID: tc.verifiedCluster, Issuer: "issuer", Subject: "subject"}, nil
			})
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(auth.ClusterIDHeader) != "" || r.Header.Get("Authorization") != "" || auth.TargetClusterFromContext(r.Context()) != "cluster-a" {
					t.Fatal("headers were not moved to private request context")
				}
				w.WriteHeader(204)
			})
			request := httptest.NewRequest("GET", "/v1/conversations", nil)
			request.Header.Set("Authorization", "Bearer bearer")
			for _, value := range tc.values {
				request.Header.Add(auth.ClusterIDHeader, value)
			}
			response := httptest.NewRecorder()
			requireAccessToken(verifier, auditLogger{logger: discardAuditLogger()}, next).ServeHTTP(response, request)
			if response.Code != tc.wantStatus || calls != tc.wantCalls {
				t.Fatalf("status=%d calls=%d", response.Code, calls)
			}
		})
	}
}

func TestTargetNodeHeaderIsCheckedBeforeProtectedOperation(t *testing.T) {
	for _, values := range [][]string{nil, {"node-b"}, {""}, {"node-b", "node-b"}, {"node-a,node-b"}, {" node-b "}, {strings.Repeat("x", 257)}, {"node\nb"}} {
		calls := 0
		wantNode := ""
		wantValid := len(values) == 0 || len(values) == 1 && values[0] == "node-b"
		if len(values) == 1 {
			wantNode = values[0]
		}
		verifier := tokenVerifierFunc(func(ctx context.Context, _ string) (auth.Identity, error) {
			calls++
			if auth.TargetNodeFromContext(ctx) != wantNode {
				t.Fatal("verifier lost node routing hint")
			}
			return auth.Identity{ClusterID: "cluster-a", Issuer: "issuer", Subject: "subject"}, nil
		})
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(auth.NodeHeader) != "" || auth.TargetNodeFromContext(r.Context()) != wantNode {
				t.Fatal("node header was not moved to private request context")
			}
			w.WriteHeader(204)
		})
		request := httptest.NewRequest("GET", "/v1/conversations", nil)
		request.Header.Set("Authorization", "Bearer bearer")
		request.Header.Set(auth.ClusterIDHeader, "cluster-a")
		for _, value := range values {
			request.Header.Add(auth.NodeHeader, value)
		}
		response := httptest.NewRecorder()
		requireAccessToken(verifier, auditLogger{logger: discardAuditLogger()}, next).ServeHTTP(response, request)
		if wantValid && (calls != 1 || response.Code != 204) || !wantValid && (calls != 0 || response.Code != 401) {
			t.Fatalf("headers=%q status=%d calls=%d", values, response.Code, calls)
		}
	}
}
