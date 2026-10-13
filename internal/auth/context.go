package auth

import (
	"context"
	"net/http"
)

type bearerTokenContextKey struct{}
type identityContextKey struct{}
type mcpHeadersContextKey struct{}

type contextWithoutAuthentication struct {
	context.Context
}

// WithBearerToken returns a context carrying a delegated OpenSVC access JWT.
// Callers must keep the returned context scoped to a single agent request.
func WithBearerToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, bearerTokenContextKey{}, token)
}

// BearerTokenFromContext returns the delegated OpenSVC access JWT.
func BearerTokenFromContext(ctx context.Context) (string, bool) {
	token, ok := ctx.Value(bearerTokenContextKey{}).(string)
	return token, ok && token != ""
}

// WithMCPHeaders carries the request headers a backend forwards to its MCP
// for one request, such as the routing hints of the om3 backend. They never
// establish identity.
func WithMCPHeaders(ctx context.Context, headers http.Header) context.Context {
	return context.WithValue(ctx, mcpHeadersContextKey{}, headers.Clone())
}

// MCPHeadersFromContext returns a copy of the headers to forward to the MCP.
func MCPHeadersFromContext(ctx context.Context) http.Header {
	headers, _ := ctx.Value(mcpHeadersContextKey{}).(http.Header)
	return headers.Clone()
}

// WithIdentity returns a context carrying the verified OpenSVC caller identity.
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	identity.Grants = append([]string(nil), identity.Grants...)
	return context.WithValue(ctx, identityContextKey{}, identity)
}

// IdentityFromContext returns a copy of the verified OpenSVC caller identity.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityContextKey{}).(Identity)
	if !ok || identity.Subject == "" {
		return Identity{}, false
	}
	identity.Grants = append([]string(nil), identity.Grants...)
	return identity, true
}

// WithoutAuthentication returns a context that preserves cancellation,
// deadlines, and unrelated values while hiding OpenSVC authentication data.
func WithoutAuthentication(ctx context.Context) context.Context {
	return contextWithoutAuthentication{Context: ctx}
}

func (c contextWithoutAuthentication) Value(key any) any {
	if _, ok := key.(bearerTokenContextKey); ok {
		return nil
	}
	if _, ok := key.(identityContextKey); ok {
		return nil
	}
	if _, ok := key.(mcpHeadersContextKey); ok {
		return nil
	}
	return c.Context.Value(key)
}
