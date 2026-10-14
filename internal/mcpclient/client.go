package mcpclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/ai-agent/internal/auth"
	"github.com/opensvc/ai-agent/internal/config"
	"github.com/opensvc/ai-agent/internal/requestid"
)

const (
	clientName    = "opensvc-ai-agent"
	clientVersion = "v0.1.0"
)

// Client creates request-scoped MCP sessions. It never retains a Bearer token.
type Client struct {
	endpoint   string
	httpClient *http.Client
}

// MCP is reached through a local Unix socket: the path selects the server and
// this fixed origin only names it in request URLs.
const (
	socketOrigin   = "http://opensvc-mcp"
	socketEndpoint = socketOrigin + "/mcp"
)

// New creates an MCP client over the trusted local Unix socket. MCP accepts
// delegated OpenSVC tokens only there; filesystem permissions protect it.
// forwardedHeaders names the headers of the backend this MCP serves: the
// client sends them only as the request context carries them.
func New(socketPath string, forwardedHeaders ...string) (*Client, error) {
	path, err := config.ParseMCPSocket(socketPath)
	if err != nil {
		return nil, fmt.Errorf("parse MCP socket: %w", err)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", path)
	}

	return &Client{
		endpoint:   socketEndpoint,
		httpClient: securedHTTPClient(&http.Client{Transport: transport}, socketOrigin, forwardedHeaders),
	}, nil
}

func securedHTTPClient(base *http.Client, origin string, forwardedHeaders []string) *http.Client {
	clientCopy := *base
	baseTransport := clientCopy.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	clientCopy.Transport = responseLimitTransport{
		base:     bearerTransport{base: baseTransport, origin: origin, forwardedHeaders: forwardedHeaders},
		maxBytes: maxMCPResponseBodyBytes,
	}
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clientCopy
}

// Connect initializes an MCP session using the delegated JWT in ctx.
func (c *Client) Connect(ctx context.Context) (*Session, error) {
	if _, ok := auth.BearerTokenFromContext(ctx); !ok {
		return nil, fmt.Errorf("connect MCP: delegated OpenSVC access JWT is missing from request context")
	}

	client := mcp.NewClient(
		&mcp.Implementation{Name: clientName, Version: clientVersion},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}},
	)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             c.endpoint,
		HTTPClient:           c.httpClient,
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect MCP: %w", err)
	}
	return &Session{session: session}, nil
}

// Session is an initialized MCP session scoped to one agent request.
type Session struct {
	session *mcp.ClientSession
}

// ListTools returns every tool exposed by the MCP server, following pagination.
func (s *Session) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	catalog := toolCatalog{tools: make([]*mcp.Tool, 0, maxMCPToolCount)}
	for tool, err := range s.session.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list MCP tools: %w", err)
		}
		if err := catalog.add(tool); err != nil {
			return nil, fmt.Errorf("list MCP tools: %w", err)
		}
	}
	return catalog.tools, nil
}

// CallTool invokes a named MCP tool with JSON-compatible arguments.
func (s *Session) CallTool(ctx context.Context, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	if name == "" {
		return nil, fmt.Errorf("call MCP tool: tool name is empty")
	}
	result, err := s.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return nil, fmt.Errorf("call MCP tool %q: %w", name, err)
	}
	return result, nil
}

// Close terminates the MCP session.
func (s *Session) Close() error {
	if err := s.session.Close(); err != nil {
		return fmt.Errorf("close MCP session: %w", err)
	}
	return nil
}

type bearerTransport struct {
	base             http.RoundTripper
	origin           string
	forwardedHeaders []string
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme+"://"+request.URL.Host != t.origin || (request.Host != "" && request.Host != request.URL.Host) {
		return nil, fmt.Errorf("send MCP request: destination differs from the configured MCP socket origin")
	}
	token, ok := auth.BearerTokenFromContext(request.Context())
	if !ok {
		return nil, fmt.Errorf("send MCP request: delegated OpenSVC access JWT is missing from request context")
	}

	requestCopy := request.Clone(request.Context())
	requestCopy.Header = request.Header.Clone()
	requestCopy.Header.Set("Authorization", "Bearer "+token)
	// The request ID joins the MCP audit lines with those of the agent.
	requestCopy.Header.Del(requestid.Header)
	if id := requestid.FromContext(request.Context()); id != "" {
		requestCopy.Header.Set(requestid.Header, id)
	}
	headers := auth.MCPHeadersFromContext(request.Context())
	for _, name := range t.forwardedHeaders {
		requestCopy.Header.Del(name)
	}
	for name, values := range headers {
		if !slices.ContainsFunc(t.forwardedHeaders, func(forwarded string) bool {
			return http.CanonicalHeaderKey(forwarded) == http.CanonicalHeaderKey(name)
		}) || len(values) != 1 || values[0] == "" {
			continue
		}
		requestCopy.Header.Set(name, values[0])
	}
	return t.base.RoundTrip(requestCopy)
}
