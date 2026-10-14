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
// identity: its contract belongs to its MCP. The answers are reused for at
// most a minute and the refusals for five seconds (identityCache); no key or
// token is cached here.
func (c *Client) WhoAmI(ctx context.Context, raw string, output any) error {
	if ctx.Err() != nil {
		return auth.ErrVerificationUnavailable
	}
	key := identityCacheKey(raw, auth.MCPHeadersFromContext(ctx))
	data, err := c.identities.get(ctx, key, func(ctx context.Context) ([]byte, error) {
		return c.whoami(ctx, raw)
	})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, output); err != nil {
		return auth.ErrVerificationUnavailable
	}
	return nil
}

// whoami asks the MCP once and returns its JSON answer.
func (c *Client) whoami(ctx context.Context, raw string) ([]byte, error) {
	endpoint, err := url.Parse(c.endpoint)
	if err != nil {
		return nil, auth.ErrVerificationUnavailable
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/auth/whoami"
	if endpoint.RawPath != "" {
		endpoint.RawPath = strings.TrimRight(endpoint.RawPath, "/") + "/auth/whoami"
	}
	ctx, stop := context.WithTimeout(ctx, identityTimeout)
	defer stop()
	request, err := http.NewRequestWithContext(auth.WithBearerToken(ctx, raw), http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, auth.ErrVerificationUnavailable
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, auth.ErrVerificationUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, auth.ErrInvalidToken
	}
	if response.StatusCode != http.StatusOK {
		return nil, auth.ErrVerificationUnavailable
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, auth.ErrVerificationUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxIdentityResponseBytes+1))
	if err != nil || len(data) > maxIdentityResponseBytes {
		return nil, auth.ErrVerificationUnavailable
	}
	if !json.Valid(data) {
		return nil, auth.ErrVerificationUnavailable
	}
	return data, nil
}
