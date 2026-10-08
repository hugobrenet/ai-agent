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
	"unicode"
	"unicode/utf8"

	"github.com/hugobrenet/opensvc-ai-agent/internal/auth"
)

const (
	identityTimeout          = 10 * time.Second
	maxIdentityResponseBytes = 16 << 10
)

// Verify treats the bearer as opaque and obtains the authenticated identity
// from MCP over its local socket. OpenSVC token rules belong to MCP/daemon.
// No keys, identities or tokens are cached here.
func (c *Client) Verify(ctx context.Context, raw string) (auth.Identity, error) {
	endpoint, err := url.Parse(c.endpoint)
	if err != nil {
		return auth.Identity{}, auth.ErrVerificationUnavailable
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/auth/whoami"
	if endpoint.RawPath != "" {
		endpoint.RawPath = strings.TrimRight(endpoint.RawPath, "/") + "/auth/whoami"
	}
	ctx, stop := context.WithTimeout(ctx, identityTimeout)
	defer stop()
	request, err := http.NewRequestWithContext(auth.WithBearerToken(ctx, raw), http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return auth.Identity{}, auth.ErrVerificationUnavailable
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
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
	// Validate the bridge response, not JWT claims. The trusted MCP owns
	// profile selection and must return the original subject, not a username.
	if !validIdentityField(identity.ClusterID) || !validIdentityField(identity.Subject) || !validIdentityField(identity.Issuer) || identity.ExpiresAt.IsZero() {
		return auth.Identity{}, auth.ErrVerificationUnavailable
	}
	if !time.Now().Before(identity.ExpiresAt) {
		return auth.Identity{}, auth.ErrInvalidToken
	}
	return identity, nil
}

func validIdentityField(value string) bool {
	return len(value) > 0 && len(value) <= 256 && value == strings.TrimSpace(value) && utf8.ValidString(value) && !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) })
}
