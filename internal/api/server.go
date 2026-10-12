package api

import (
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
// authenticate with its verifier and run on its conversations only: the
// route chooses the backend, never the request.
type Backend struct {
	Name          string
	Conversations ConversationService
	Verifier      auth.TokenVerifier
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
	for _, backend := range backends {
		if !conversation.ValidBackendName(backend.Name) || names[backend.Name] {
			return nil, fmt.Errorf("API backend name %q is invalid or duplicated", backend.Name)
		}
		names[backend.Name] = true
		if backend.Conversations == nil {
			return nil, fmt.Errorf("API backend %s conversation service is nil", backend.Name)
		}
		if backend.Verifier == nil {
			return nil, fmt.Errorf("API backend %s token verifier is nil", backend.Name)
		}
		registerBackend(mux, backend, limiter, auditLogger{logger: config.AuditLogger.With(slog.String("backend", backend.Name))})
	}
	handler, err := withCORS(mux, config.CORSAllowedOrigins)
	if err != nil {
		return nil, err
	}
	return withRequestID(handler), nil
}

func registerBackend(mux *http.ServeMux, backend Backend, limiter *askLimiter, audit auditLogger) {
	prefix := "/v1/" + backend.Name
	conversations := backend.Conversations
	handle := func(method string, path string, handler http.Handler) {
		mux.Handle(method+" "+prefix+path, requireAccessToken(backend.Verifier, audit, handler))
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
