package auth

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrInvalidToken            = errors.New("invalid OpenSVC access token")
	ErrVerificationUnavailable = errors.New("OpenSVC identity verification unavailable")
)

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

// Delegation contains unverified claims. Only a successful daemon identity
// check through the trusted MCP can turn these claims into an Identity.
type Delegation struct {
	ClusterID string
	Subject   string
	Issuer    string
	ExpiresAt time.Time
}

type jwtClaims struct {
	ClusterID string `json:"cluster_id"`
	TokenUse  string `json:"token_use"`
	jwt.RegisteredClaims
}

func CheckDelegation(raw string) (Delegation, error) {
	if raw == "" || len(raw) > 16<<10 {
		return Delegation{}, ErrInvalidToken
	}
	var claims jwtClaims
	token, _, err := jwt.NewParser().ParseUnverified(raw, &claims)
	if err != nil || token == nil || token.Method != jwt.SigningMethodRS256 {
		return Delegation{}, ErrInvalidToken
	}
	if err := jwt.NewValidator(jwt.WithExpirationRequired()).Validate(&claims); err != nil {
		return Delegation{}, ErrInvalidToken
	}
	if !validClaim(claims.ClusterID) || !validClaim(claims.Subject) || !validClaim(claims.Issuer) || claims.TokenUse != "access" || claims.ExpiresAt == nil {
		return Delegation{}, ErrInvalidToken
	}
	return Delegation{ClusterID: claims.ClusterID, Subject: claims.Subject, Issuer: claims.Issuer, ExpiresAt: claims.ExpiresAt.Time}, nil
}

func validClaim(value string) bool {
	return len(value) > 0 && len(value) <= 256 && value == strings.TrimSpace(value) && utf8.ValidString(value) && !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) })
}
