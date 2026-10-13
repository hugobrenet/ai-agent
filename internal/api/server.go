package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/opensvc/ai-agent/internal/auth"
	"github.com/opensvc/ai-agent/internal/conversation"
)

type HealthResponse struct {
	Status string `json:"status"`
}

type HandlerConfig struct {
	MaxConcurrentAsks  int
	AuditLogger        *slog.Logger
	CORSAllowedOrigins []string
}

// Backend is one product the agent serves under /v1/{Name}. Its routes
// authenticate with its authenticator and run on its conversations only: the
// route chooses the backend, never the request.
type Backend struct {
	Name          string
	Conversations ConversationService
	Authenticator Authenticator
}

// Authenticator establishes the identity of a request to one backend, through
// the whoami bridge of its MCP. The API knows no backend header: each
// authenticator reads its own.
type Authenticator interface {
	// Headers names the request headers the backend reads, besides
	// Authorization. CORS allows them and they are removed once read.
	Headers() []string
	// Authenticate verifies the bearer token with the backend headers. It
	// returns auth.ErrVerificationUnavailable when the identity cannot be
	// established, and any other error for a refused request. The returned
	// context carries what the backend's MCP requests need.
	Authenticate(ctx context.Context, header http.Header, token string) (context.Context, auth.Identity, error)
}

func NewHandler(backends []Backend, config HandlerConfig) (http.Handler, error) {
	if len(backends) == 0 {
		return nil, fmt.Errorf("API has no backend")
	}
	if config.MaxConcurrentAsks <= 0 {
		return nil, fmt.Errorf("API max concurrent asks must be positive")
	}
	if config.AuditLogger == nil {
		return nil, fmt.Errorf("API audit logger is nil")
	}
	limiter := newAskLimiter(config.MaxConcurrentAsks)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", getHealth)
	names := make(map[string]bool, len(backends))
	var headers []string
	for _, backend := range backends {
		if !conversation.ValidBackendName(backend.Name) || names[backend.Name] {
			return nil, fmt.Errorf("API backend name %q is invalid or duplicated", backend.Name)
		}
		names[backend.Name] = true
		if backend.Conversations == nil {
			return nil, fmt.Errorf("API backend %s conversation service is nil", backend.Name)
		}
		if backend.Authenticator == nil {
			return nil, fmt.Errorf("API backend %s authenticator is nil", backend.Name)
		}
		headers = append(headers, backend.Authenticator.Headers()...)
		registerBackend(mux, backend, limiter, auditLogger{logger: config.AuditLogger.With(slog.String("backend", backend.Name))})
	}
	handler, err := withCORS(mux, config.CORSAllowedOrigins, headers)
	if err != nil {
		return nil, err
	}
	return withRequestID(handler), nil
}

func registerBackend(mux *http.ServeMux, backend Backend, limiter *askLimiter, audit auditLogger) {
	prefix := "/v1/" + backend.Name
	conversations := backend.Conversations
	handle := func(method string, path string, handler http.Handler) {
		mux.Handle(method+" "+prefix+path, requireAccessToken(backend.Authenticator, audit, handler))
	}
	handle("POST", "/conversations", serveCreateConversation(conversations, audit))
	handle("GET", "/conversations", serveListConversations(conversations, audit))
	handle("GET", "/conversations/{id}", serveGetConversation(conversations, audit))
	handle("GET", "/conversations/{id}/messages", serveConversationMessages(conversations, audit))
	handle("PATCH", "/conversations/{id}", serveUpdateConversationTitle(conversations, audit))
	handle("DELETE", "/conversations/{id}", serveDeleteConversation(conversations, audit))
	handle("POST", "/conversations/{id}/turns", serveConversationTurn(conversations, limiter, audit))
	handle("POST", "/conversations/{id}/turns/{turn_id}/confirmation", serveConversationConfirmation(conversations, limiter, audit))
}

func getHealth(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(HealthResponse{Status: "ok"})
}
