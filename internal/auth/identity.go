package auth

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidToken            = errors.New("invalid OpenSVC access token")
	ErrVerificationUnavailable = errors.New("OpenSVC identity verification unavailable")
)

// Identity is established by the configured MCP's whoami bridge, not by
// locally decoding the bearer token.
type Identity struct {
	ClusterID string    `json:"cluster_id"`
	Subject   string    `json:"subject"`
	Issuer    string    `json:"issuer"`
	Grants    []string  `json:"-"`
	ExpiresAt time.Time `json:"expires_at"`
}

type TokenVerifier interface {
	Verify(context.Context, string) (Identity, error)
}
