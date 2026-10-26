package api

import (
	"context"
	"log"
	"log/slog"
	"net/http"

	"github.com/opensvc/ai-agent/internal/agent"
	"github.com/opensvc/ai-agent/internal/requestid"
)

// ToolLister lists the tools of the MCP of a backend, as the user of ctx.
type ToolLister interface {
	Tools(context.Context) ([]agent.ToolInfo, error)
}

type ToolResponse struct {
	Name              string `json:"name"`
	Title             string `json:"title"`
	Summary           string `json:"summary"`
	ReadOnly          bool   `json:"read_only"`
	NeedsConfirmation bool   `json:"needs_confirmation"`
}

type ToolListResponse struct {
	Tools []ToolResponse `json:"tools"`
}

// serveTools lists the tools the model may be offered, the catalog of the
// deployment: a user reads what the assistant can do, not what the rights
// of the user allow, which the daemon checks at the call.
func serveTools(tools ToolLister, audit auditLogger) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if tools == nil {
			writeJSONError(response, http.StatusNotFound, "not_found", "this backend lists no tools")
			return
		}
		items, err := tools.Tools(request.Context())
		if err != nil {
			// The cause in the server log; the audit keeps the code only.
			log.Printf("list the tools of the MCP, request %s: %v", requestid.FromContext(request.Context()), err)
			audit.event(request.Context(), "tools_list_failed", slog.Int("status", http.StatusBadGateway), slog.String("code", "tools_unavailable"))
			writeJSONError(response, http.StatusBadGateway, "tools_unavailable", "the tools of the MCP could not be read")
			return
		}
		result := make([]ToolResponse, 0, len(items))
		for _, item := range items {
			result = append(result, ToolResponse{Name: item.Name, Title: item.Title, Summary: item.Summary, ReadOnly: item.ReadOnly, NeedsConfirmation: item.Confirmable})
		}
		audit.event(request.Context(), "tools_listed", slog.Int("count", len(result)))
		response.Header().Set("Cache-Control", "no-store")
		writeJSON(response, http.StatusOK, ToolListResponse{Tools: result})
	}
}
