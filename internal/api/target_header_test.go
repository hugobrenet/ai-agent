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
