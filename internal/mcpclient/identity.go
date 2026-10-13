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

	"github.com/opensvc/ai-agent/internal/auth"
)

const (
	identityTimeout          = 10 * time.Second
	maxIdentityResponseBytes = 16 << 10
)

// WhoAmI treats the bearer as opaque and decodes into output the identity the
// MCP's whoami bridge answers for it, over the local socket, with the headers
// the request context carries for this MCP. The backend validates the
// identity: its contract belongs to its MCP. No keys, identities or tokens
// are cached here.
func (c *Client) WhoAmI(ctx context.Context, raw string, output any) error {
	endpoint, err := url.Parse(c.endpoint)
	if err != nil {
		return auth.ErrVerificationUnavailable
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/auth/whoami"
	if endpoint.RawPath != "" {
		endpoint.RawPath = strings.TrimRight(endpoint.RawPath, "/") + "/auth/whoami"
	}
	ctx, stop := context.WithTimeout(ctx, identityTimeout)
	defer stop()
	request, err := http.NewRequestWithContext(auth.WithBearerToken(ctx, raw), http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return auth.ErrVerificationUnavailable
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return auth.ErrVerificationUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return auth.ErrInvalidToken
	}
	if response.StatusCode != http.StatusOK {
		return auth.ErrVerificationUnavailable
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return auth.ErrVerificationUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxIdentityResponseBytes+1))
	if err != nil || len(data) > maxIdentityResponseBytes {
		return auth.ErrVerificationUnavailable
	}
	if err := json.Unmarshal(data, output); err != nil {
		return auth.ErrVerificationUnavailable
	}
	return nil
}
