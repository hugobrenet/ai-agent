// Package oc3 is the oc3 backend of the agent: its MCP serves the OpenSVC
// collector. A request carries only the OpenID Connect access token of its
// user, which the collector verifies; it names no cluster or node.
package oc3

import (
	"context"
	"net/http"
	"time"

	"github.com/opensvc/ai-agent/internal/auth"
	"github.com/opensvc/ai-agent/internal/mcpclient"
)

// NewMCPClient returns the client of the oc3 MCP socket, which forwards no
// header besides the token.
func NewMCPClient(socketPath string) (*mcpclient.Client, error) {
	return mcpclient.New(socketPath)
}

// Authenticator establishes the identity of an oc3 request through the whoami
// bridge of the oc3 MCP.
type Authenticator struct {
	mcp *mcpclient.Client
}

func NewAuthenticator(mcp *mcpclient.Client) *Authenticator {
	return &Authenticator{mcp: mcp}
}

// Headers is empty: an oc3 request carries no header besides Authorization.
func (a *Authenticator) Headers() []string {
	return nil
}

type identityResponse struct {
	Issuer    string    `json:"issuer"`
	Subject   string    `json:"subject"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Authenticate asks the MCP who the token is. The MCP takes OpenID Connect
// access tokens only, and the collector verifies them; the agent treats the
// token as opaque.
func (a *Authenticator) Authenticate(ctx context.Context, _ http.Header, token string) (context.Context, auth.Identity, error) {
	var response identityResponse
	if err := a.mcp.WhoAmI(ctx, token, &response); err != nil {
		return nil, auth.Identity{}, err
	}
	// Validate the bridge response, not JWT claims: the MCP returns the issuer
	// and the subject the collector confirmed for this token.
	if !auth.ValidIdentityValue(response.Issuer) || !auth.ValidIdentityValue(response.Subject) || response.ExpiresAt.IsZero() {
		return nil, auth.Identity{}, auth.ErrVerificationUnavailable
	}
	if !time.Now().Before(response.ExpiresAt) {
		return nil, auth.Identity{}, auth.ErrInvalidToken
	}
	return ctx, auth.Identity{Issuer: response.Issuer, Subject: response.Subject, ExpiresAt: response.ExpiresAt}, nil
}
