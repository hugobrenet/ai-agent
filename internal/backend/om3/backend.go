// Package om3 is the om3 backend of the agent: its MCP serves the OpenSVC
// daemons of clusters. A request names its target cluster, whose daemon
// verifies the token, and may name a node as a routing hint.
package om3

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/opensvc/ai-agent/internal/auth"
	"github.com/opensvc/ai-agent/internal/mcpclient"
)

const (
	// ClusterIDHeader selects the cluster whose daemon verifies the token. It
	// is required, and never trusted as an identity on its own.
	ClusterIDHeader = "X-OpenSVC-Cluster-ID"
	// NodeHeader is an optional routing hint, never an identity.
	NodeHeader = "X-OpenSVC-Node"
)

// NewMCPClient returns the client of the om3 MCP socket, which forwards the
// target cluster and node of each request.
func NewMCPClient(socketPath string) (*mcpclient.Client, error) {
	return mcpclient.New(socketPath, ClusterIDHeader, NodeHeader)
}

// Authenticator establishes the identity of an om3 request through the
// whoami bridge of the om3 MCP.
type Authenticator struct {
	mcp *mcpclient.Client
}

func NewAuthenticator(mcp *mcpclient.Client) *Authenticator {
	return &Authenticator{mcp: mcp}
}

// Headers are the request headers of the om3 backend.
func (a *Authenticator) Headers() []string {
	return []string{ClusterIDHeader, NodeHeader}
}

type identityResponse struct {
	ClusterID string    `json:"cluster_id"`
	Subject   string    `json:"subject"`
	Issuer    string    `json:"issuer"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Authenticate reads the target cluster, required, and node, optional, then
// asks the MCP who the token is on that cluster. The returned context
// carries the target for the MCP requests of the conversation.
func (a *Authenticator) Authenticate(ctx context.Context, header http.Header, token string) (context.Context, auth.Identity, error) {
	cluster, clusterErr := targetFromHeader(header, ClusterIDHeader)
	node, nodeErr := targetFromHeader(header, NodeHeader)
	// The cluster ID selects which daemon verifies the token: it is required.
	if clusterErr != nil || nodeErr != nil || cluster == "" {
		return nil, auth.Identity{}, auth.ErrInvalidToken
	}
	targets := http.Header{}
	targets.Set(ClusterIDHeader, cluster)
	if node != "" {
		targets.Set(NodeHeader, node)
	}
	ctx = auth.WithMCPHeaders(ctx, targets)
	var response identityResponse
	if err := a.mcp.WhoAmI(ctx, token, &response); err != nil {
		return nil, auth.Identity{}, err
	}
	// Validate the bridge response, not JWT claims. The trusted MCP owns
	// profile selection and must return the original subject, not a username.
	if !auth.ValidIdentityValue(response.ClusterID) || !auth.ValidIdentityValue(response.Subject) || !auth.ValidIdentityValue(response.Issuer) || response.ExpiresAt.IsZero() {
		return nil, auth.Identity{}, auth.ErrVerificationUnavailable
	}
	if !time.Now().Before(response.ExpiresAt) || response.ClusterID != cluster {
		return nil, auth.Identity{}, auth.ErrInvalidToken
	}
	return ctx, auth.Identity{ClusterID: response.ClusterID, Subject: response.Subject, Issuer: response.Issuer, ExpiresAt: response.ExpiresAt}, nil
}

// targetFromHeader reads an untrusted routing value. Combined or duplicate
// headers cannot select a target.
func targetFromHeader(header http.Header, name string) (string, error) {
	values := header.Values(name)
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 || !validTarget(values[0]) {
		return "", auth.ErrInvalidToken
	}
	return values[0], nil
}

func validTarget(value string) bool {
	return auth.ValidIdentityValue(value) && !strings.Contains(value, ",")
}
