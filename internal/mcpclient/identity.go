package mcpclient

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hugobrenet/opensvc-ai-agent/internal/auth"
)

const (
	identityTimeout          = 10 * time.Second
	maxIdentityResponseBytes = 16 << 10
)

// Verify delegates native JWT signature verification to the daemon through a
// narrow HTTPS MCP route. No keys, identities or tokens are cached here.
func (c *Client) Verify(ctx context.Context, raw string) (auth.Identity, error) {
	delegation, err := auth.CheckDelegation(raw)
	if err != nil {
		return auth.Identity{}, err
	}
	endpoint, err := url.Parse(c.endpoint)
	if err != nil {
		return auth.Identity{}, auth.ErrVerificationUnavailable
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/auth/whoami"
	if endpoint.RawPath != "" {
		endpoint.RawPath = strings.TrimRight(endpoint.RawPath, "/") + "/auth/whoami"
	}
	ctx, cancel := context.WithDeadline(ctx, delegation.ExpiresAt)
	defer cancel()
	ctx, stop := context.WithTimeout(ctx, identityTimeout)
	defer stop()
	request, err := http.NewRequestWithContext(auth.WithBearerToken(ctx, raw), http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return auth.Identity{}, auth.ErrVerificationUnavailable
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		if !time.Now().Before(delegation.ExpiresAt) {
			return auth.Identity{}, auth.ErrInvalidToken
		}
		return auth.Identity{}, auth.ErrVerificationUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return auth.Identity{}, auth.ErrInvalidToken
	}
	if response.StatusCode != http.StatusOK {
		return auth.Identity{}, auth.ErrVerificationUnavailable
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return auth.Identity{}, auth.ErrVerificationUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxIdentityResponseBytes+1))
	if err != nil || len(data) > maxIdentityResponseBytes {
		return auth.Identity{}, auth.ErrVerificationUnavailable
	}
	var identity auth.Identity
	if err := json.Unmarshal(data, &identity); err != nil {
		return auth.Identity{}, auth.ErrVerificationUnavailable
	}
	// The trusted MCP must confirm exactly this request's native identity.
	if identity.ClusterID != delegation.ClusterID || identity.Subject != delegation.Subject || identity.Issuer != delegation.Issuer || !identity.ExpiresAt.Equal(delegation.ExpiresAt) || !time.Now().Before(identity.ExpiresAt) {
		return auth.Identity{}, auth.ErrInvalidToken
	}
	return identity, nil
}
