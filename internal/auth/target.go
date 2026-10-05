package auth

import (
	"net/http"
	"strings"
)

const ClusterIDHeader = "X-OpenSVC-Cluster-ID"

// TargetClusterFromHeader reads an optional, untrusted routing hint. A single
// exact value is required; combined/duplicate headers cannot select a target.
func TargetClusterFromHeader(header http.Header) (string, error) {
	values := header.Values(ClusterIDHeader)
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 || !validClaim(values[0]) || strings.Contains(values[0], ",") {
		return "", ErrInvalidToken
	}
	return values[0], nil
}
