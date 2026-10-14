// Package requestid carries the ID the agent gives each request it serves.
// The API returns it to the client and writes it on every audit line; the MCP
// client forwards it to the MCPs, whose audit lines carry it too.
package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

const (
	// Header names the request ID on the API responses and on the requests
	// to the MCPs.
	Header = "X-Request-ID"
	// Bytes is the size of a request ID before its hexadecimal encoding.
	Bytes = 16
)

type contextKey struct{}

// New returns a random request ID of 32 lowercase hexadecimal characters.
func New() (string, error) {
	data := make([]byte, Bytes)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

// With returns ctx carrying the request ID.
func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext returns the request ID of ctx, empty when it has none.
func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}
