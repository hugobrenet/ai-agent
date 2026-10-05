package mcpclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/hugobrenet/opensvc-ai-agent/internal/auth"
	"github.com/hugobrenet/opensvc-ai-agent/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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

// New creates a verified HTTPS MCP client. An empty CA file uses system roots.
func New(endpoint string, caFile string) (*Client, error) {
	parsed, err := config.ParseMCPURL(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse MCP URL: %w", err)
	}
	var roots *x509.CertPool
	if caFile != "" {
		file, err := os.Open(caFile)
		if err != nil {
			return nil, fmt.Errorf("open MCP CA file: %w", err)
		}
		defer file.Close()
		const maxCABytes = 1 << 20
		data, err := io.ReadAll(io.LimitReader(file, maxCABytes+1))
		if err != nil {
			return nil, fmt.Errorf("read MCP CA file: %w", err)
		}
		roots = x509.NewCertPool()
		if len(data) > maxCABytes || !roots.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("MCP CA file must contain PEM certificates and be at most 1 MiB")
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}

	return &Client{
		endpoint:   parsed.String(),
		httpClient: securedHTTPClient(&http.Client{Transport: transport}, parsed.Scheme+"://"+parsed.Host),
	}, nil
}

func securedHTTPClient(base *http.Client, origin string) *http.Client {
	clientCopy := *base
	baseTransport := clientCopy.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	clientCopy.Transport = responseLimitTransport{
		base:     bearerTransport{base: baseTransport, origin: origin},
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
	base   http.RoundTripper
	origin string
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme+"://"+request.URL.Host != t.origin || (request.Host != "" && request.Host != request.URL.Host) {
		return nil, fmt.Errorf("send MCP request: destination differs from configured HTTPS origin")
	}
	token, ok := auth.BearerTokenFromContext(request.Context())
	if !ok {
		return nil, fmt.Errorf("send MCP request: delegated OpenSVC access JWT is missing from request context")
	}

	requestCopy := request.Clone(request.Context())
	requestCopy.Header = request.Header.Clone()
	requestCopy.Header.Set("Authorization", "Bearer "+token)
	requestCopy.Header.Del(auth.ClusterIDHeader)
	if clusterID := auth.TargetClusterFromContext(request.Context()); clusterID != "" {
		requestCopy.Header.Set(auth.ClusterIDHeader, clusterID)
	}
	return t.base.RoundTrip(requestCopy)
}
