package auth

import (
	"net/http"
	"strings"
)

const (
	ClusterIDHeader = "X-OpenSVC-Cluster-ID"
	NodeHeader      = "X-OpenSVC-Node"
)

// TargetClusterFromHeader reads an optional, untrusted routing hint. A single
// exact value is required; combined/duplicate headers cannot select a target.
func TargetClusterFromHeader(header http.Header) (string, error) {
	return targetFromHeader(header, ClusterIDHeader)
}

// TargetNodeFromHeader reads the optional node routing hint with the same
// strict bounds as the cluster header. Neither header establishes identity.
func TargetNodeFromHeader(header http.Header) (string, error) {
	return targetFromHeader(header, NodeHeader)
}

func validTarget(value string) bool {
	return validClaim(value) && !strings.Contains(value, ",")
}

func targetFromHeader(header http.Header, name string) (string, error) {
	values := header.Values(name)
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 || !validTarget(values[0]) {
		return "", ErrInvalidToken
	}
	return values[0], nil
}
