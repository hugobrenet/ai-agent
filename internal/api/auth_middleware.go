package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/opensvc/ai-agent/internal/auth"
)

const maxBearerTokenBytes = 16 << 10

func requireAccessToken(verifier auth.TokenVerifier, audit auditLogger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		rawToken, ok := bearerToken(request.Header.Get("Authorization"))
		targetCluster, targetErr := auth.TargetClusterFromHeader(request.Header)
		targetNode, nodeErr := auth.TargetNodeFromHeader(request.Header)
		// The cluster ID selects which daemon verifies the token: it is required.
		if !ok || targetErr != nil || nodeErr != nil || targetCluster == "" || len(rawToken) > maxBearerTokenBytes || len(request.Header.Values("Authorization")) != 1 || request.URL.Query().Has("access_token") {
			audit.event(request.Context(), "auth_rejected",
				slog.Int("status", http.StatusUnauthorized),
				slog.String("code", "unauthorized"),
			)
			writeUnauthorized(response)
			return
		}
		ctx := auth.WithTargetCluster(request.Context(), targetCluster)
		ctx = auth.WithTargetNode(ctx, targetNode)
		identity, err := verifier.Verify(ctx, rawToken)
		if errors.Is(err, auth.ErrVerificationUnavailable) {
			audit.event(request.Context(), "auth_unavailable", slog.Int("status", http.StatusServiceUnavailable), slog.String("code", "authentication_unavailable"))
			response.Header().Set("Retry-After", "1")
			writeJSONError(response, http.StatusServiceUnavailable, "authentication_unavailable", "OpenSVC identity verification is unavailable")
			return
		}
		if err != nil || identity.Subject == "" || identity.Issuer == "" || identity.ClusterID != targetCluster {
			audit.event(request.Context(), "auth_rejected",
				slog.Int("status", http.StatusUnauthorized),
				slog.String("code", "unauthorized"),
			)
			writeUnauthorized(response)
			return
		}
		if !identity.ExpiresAt.IsZero() {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, identity.ExpiresAt)
			defer cancel()
		}
		ctx = auth.WithBearerToken(ctx, rawToken)
		ctx = auth.WithIdentity(ctx, identity)
		request.Header.Del("Authorization")
		request.Header.Del(auth.ClusterIDHeader)
		request.Header.Del(auth.NodeHeader)
		next.ServeHTTP(response, request.WithContext(ctx))
	})
}

func bearerToken(authorization string) (string, bool) {
	fields := strings.Fields(authorization)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || fields[1] == "" {
		return "", false
	}
	return fields[1], true
}

func writeUnauthorized(response http.ResponseWriter) {
	response.Header().Set("WWW-Authenticate", "Bearer")
	writeJSONError(response, http.StatusUnauthorized, "unauthorized", "a valid OpenSVC access token and its cluster ID are required")
}
