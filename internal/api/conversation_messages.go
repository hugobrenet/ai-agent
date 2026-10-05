package api

import (
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/hugobrenet/opensvc-ai-agent/internal/auth"
	"github.com/hugobrenet/opensvc-ai-agent/internal/conversation"
)

func serveConversationMessages(service ConversationService, audit auditLogger) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		identity, ok := auth.IdentityFromContext(request.Context())
		if !ok {
			writeUnauthorized(response)
			return
		}
		id := request.PathValue("id")
		query, err := decodeMessageQuery(request.URL.RawQuery)
		if err != nil {
			writeConversationError(response, request, audit, "conversation_messages_rejected", err, id)
			return
		}
		page, err := service.Messages(request.Context(), identity, id, query)
		if err != nil {
			writeConversationError(response, request, audit, "conversation_messages_rejected", err, id)
			return
		}
		if page.Messages == nil {
			page.Messages = []conversation.DisplayMessage{}
		}
		audit.event(request.Context(), "conversation_messages_read",
			slog.String("conversation_id", boundedAuditID(id)), slog.Int("count", len(page.Messages)))
		writeJSON(response, http.StatusOK, page)
	}
}

func decodeMessageQuery(raw string) (conversation.MessageQuery, error) {
	if len(raw) > 256 {
		return conversation.MessageQuery{}, conversation.ErrInvalid
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return conversation.MessageQuery{}, conversation.ErrInvalid
	}
	query := conversation.MessageQuery{Limit: conversation.DefaultMessagePageLimit}
	for key, value := range values {
		if len(value) != 1 || value[0] == "" {
			return conversation.MessageQuery{}, conversation.ErrInvalid
		}
		switch key {
		case "limit":
			limit, err := strconv.Atoi(value[0])
			if err != nil || limit < 1 || limit > conversation.MaxMessagePageLimit || strconv.Itoa(limit) != value[0] {
				return conversation.MessageQuery{}, conversation.ErrInvalid
			}
			query.Limit = limit
		case "before":
			query.Before = value[0]
		default:
			return conversation.MessageQuery{}, conversation.ErrInvalid
		}
	}
	if _, _, _, err := query.Bounds(); err != nil {
		return conversation.MessageQuery{}, err
	}
	return query, nil
}
