package auth

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidToken            = errors.New("invalid OpenSVC access token")
	ErrVerificationUnavailable = errors.New("OpenSVC identity verification unavailable")
)

// Identity is established by the whoami bridge of the backend's MCP, not by
// locally decoding the bearer token. ClusterID is set by the backends whose
// identities are bound to a cluster, such as om3.
type Identity struct {
	ClusterID string    `json:"cluster_id"`
	Subject   string    `json:"subject"`
	Issuer    string    `json:"issuer"`
	Grants    []string  `json:"-"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ValidIdentityValue accepts a bounded identity field without surrounding
// spaces, control or format characters.
func ValidIdentityValue(value string) bool {
	return len(value) > 0 && len(value) <= 256 && value == strings.TrimSpace(value) && utf8.ValidString(value) && !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) })
}
