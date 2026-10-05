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

// CheckDelegation only checks the token shape and proposed target. The MCP
// must authenticate the exact token and confirm the returned identity.
func CheckDelegation(raw, targetCluster, targetNode string) (Delegation, error) {
	if raw == "" || len(raw) > 16<<10 {
		return Delegation{}, ErrInvalidToken
	}
	if targetCluster != "" && !validTarget(targetCluster) || targetNode != "" && !validTarget(targetNode) {
		return Delegation{}, ErrInvalidToken
	}
	var claims jwtClaims
	token, _, err := jwt.NewParser().ParseUnverified(raw, &claims)
	if err != nil || token == nil {
		return Delegation{}, ErrInvalidToken
	}
	if err := jwt.NewValidator(jwt.WithExpirationRequired()).Validate(&claims); err != nil {
		return Delegation{}, ErrInvalidToken
	}
	if !validClaim(claims.Subject) || !validClaim(claims.Issuer) || claims.ExpiresAt == nil {
		return Delegation{}, ErrInvalidToken
	}
	if claims.ClusterID != "" || claims.TokenUse != "" {
		// Native markers never fall back to OpenID after a failed check.
		if token.Method != jwt.SigningMethodRS256 || !validClaim(claims.ClusterID) || claims.TokenUse != "access" || targetCluster != "" && targetCluster != claims.ClusterID {
			return Delegation{}, ErrInvalidToken
		}
		targetCluster = claims.ClusterID
		if targetNode != "" && targetNode != claims.Issuer {
			return Delegation{}, ErrInvalidToken
		}
	} else {
		// OpenID routing is supplied separately. Neither the issuer nor the
		// audience is an endpoint; only the MCP catalogue can select a daemon.
		if targetCluster == "" || targetNode == "" || len(claims.Audience) == 0 || !openIDSigningMethod(token.Method.Alg()) {
			return Delegation{}, ErrInvalidToken
		}
		kid, ok := token.Header["kid"].(string)
		if !ok || !validClaim(kid) {
			return Delegation{}, ErrInvalidToken
		}
		for _, audience := range claims.Audience {
			if !validClaim(audience) {
				return Delegation{}, ErrInvalidToken
			}
		}
	}
	return Delegation{ClusterID: targetCluster, Subject: claims.Subject, Issuer: claims.Issuer, ExpiresAt: claims.ExpiresAt.Time}, nil
}

func openIDSigningMethod(algorithm string) bool {
	switch algorithm {
	case "RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512":
		return true
	default:
		return false
	}
}

func validClaim(value string) bool {
	return len(value) > 0 && len(value) <= 256 && value == strings.TrimSpace(value) && utf8.ValidString(value) && !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) })
}
