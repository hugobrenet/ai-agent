package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/opensvc/ai-agent/internal/agent"
	"github.com/opensvc/ai-agent/internal/auth"
	"github.com/opensvc/ai-agent/internal/conversation"
	"github.com/opensvc/ai-agent/internal/llm"
)

const maxConversationTitleRequestBytes = 4 << 10

type ConversationService interface {
	Create(context.Context, auth.Identity) (conversation.Conversation, error)
	Get(context.Context, auth.Identity, string) (conversation.Conversation, error)
	List(context.Context, auth.Identity) ([]conversation.Conversation, error)
	Messages(context.Context, auth.Identity, string, conversation.MessageQuery) (conversation.MessagePage, error)
	UpdateTitle(context.Context, auth.Identity, string, string) (conversation.Conversation, error)
	Delete(context.Context, auth.Identity, string) error
	PrepareTurn(context.Context, auth.Identity, string, string) (conversation.TurnExecution, error)
	PrepareConfirmation(context.Context, auth.Identity, string, string, string, agent.Decision, json.RawMessage) (conversation.TurnExecution, error)
	PendingConfirmation(context.Context, auth.Identity, string) (*conversation.PendingConfirmation, error)
	PendingClientCalls(context.Context, auth.Identity, string) ([]string, error)
	AbandonClientCalls(context.Context, auth.Identity, string) error
	PrepareClientResults(context.Context, auth.Identity, string, []llm.ToolResult) (conversation.TurnExecution, error)
	SetFeedback(context.Context, auth.Identity, string, string, conversation.Feedback) (conversation.FeedbackRecord, error)
	DeleteFeedback(context.Context, auth.Identity, string, string) error
}

type ConversationResponse struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	StoredBytes int64     `json:"stored_bytes"`
}

type ConversationTitleRequest struct {
	Title string `json:"title"`
}

type ConversationEnvelope struct {
	Conversation ConversationResponse `json:"conversation"`
	// PendingConfirmation is the tool call the conversation waits on, for a
	// client to show it again. Only the conversation read returns it.
	PendingConfirmation *Confirmation `json:"pending_confirmation,omitempty"`
}

type ConversationListResponse struct {
	Conversations []ConversationResponse `json:"conversations"`
}

func serveCreateConversation(service ConversationService, audit auditLogger) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if request.ContentLength != 0 {
			audit.event(request.Context(), "conversation_create_rejected",
				slog.Int("status", http.StatusBadRequest), slog.String("code", "invalid_request"),
			)
			writeJSONError(response, http.StatusBadRequest, "invalid_request", "conversation creation does not accept a request body")
			return
		}
		identity, ok := auth.IdentityFromContext(request.Context())
		if !ok {
			writeUnauthorized(response)
			return
		}
		item, err := service.Create(request.Context(), identity)
		if err != nil {
			writeConversationError(response, request, audit, "conversation_create_rejected", err, "")
			return
		}
		audit.event(request.Context(), "conversation_created", slog.String("conversation_id", boundedAuditID(item.ID)))
		writeJSON(response, http.StatusCreated, ConversationEnvelope{Conversation: newConversationResponse(item)})
	}
}

func serveListConversations(service ConversationService, audit auditLogger) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		identity, ok := auth.IdentityFromContext(request.Context())
		if !ok {
			writeUnauthorized(response)
			return
		}
		items, err := service.List(request.Context(), identity)
		if err != nil {
			writeConversationError(response, request, audit, "conversation_list_rejected", err, "")
			return
		}
		result := make([]ConversationResponse, 0, len(items))
		for _, item := range items {
			result = append(result, newConversationResponse(item))
		}
		audit.event(request.Context(), "conversations_listed", slog.Int("count", len(result)))
		writeJSON(response, http.StatusOK, ConversationListResponse{Conversations: result})
	}
}

func serveGetConversation(service ConversationService, audit auditLogger) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		identity, ok := auth.IdentityFromContext(request.Context())
		if !ok {
			writeUnauthorized(response)
			return
		}
		id := request.PathValue("id")
		item, err := service.Get(request.Context(), identity, id)
		if err != nil {
			writeConversationError(response, request, audit, "conversation_get_rejected", err, id)
			return
		}
		pending, err := service.PendingConfirmation(request.Context(), identity, id)
		if err != nil {
			writeConversationError(response, request, audit, "conversation_get_rejected", err, id)
			return
		}
		envelope := ConversationEnvelope{Conversation: newConversationResponse(item)}
		if pending != nil {
			envelope.PendingConfirmation = &Confirmation{
				ID: pending.ConfirmationID, TurnID: pending.TurnID, Arguments: pending.Arguments, ExpiresAt: pending.ExpiresAt,
				Tool: ConfirmationTool{Name: pending.ToolName, Title: pending.ToolTitle, Destructive: true},
			}
			// The suspended turn holds the argument types; one that does not
			// decode gives none, the confirmation staying valid.
			var state agent.SuspendedTurn
			if json.Unmarshal(pending.State, &state) == nil {
				envelope.PendingConfirmation.ArgumentTypes = state.ArgumentTypes
				envelope.PendingConfirmation.InputSchema = state.InputSchema
				envelope.PendingConfirmation.Editable = len(state.InputSchema) > 0
				envelope.PendingConfirmation.Preview = state.Preview
				envelope.PendingConfirmation.PreviewError = state.PreviewError
				envelope.PendingConfirmation.PreviewArguments = state.PreviewArguments
			}
		}
		audit.event(request.Context(), "conversation_read", slog.String("conversation_id", boundedAuditID(id)))
		writeJSON(response, http.StatusOK, envelope)
	}
}

func serveDeleteConversation(service ConversationService, audit auditLogger) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		identity, ok := auth.IdentityFromContext(request.Context())
		if !ok {
			writeUnauthorized(response)
			return
		}
		id := request.PathValue("id")
		if err := service.Delete(request.Context(), identity, id); err != nil {
			writeConversationError(response, request, audit, "conversation_delete_rejected", err, id)
			return
		}
		audit.event(request.Context(), "conversation_deleted", slog.String("conversation_id", boundedAuditID(id)))
		response.WriteHeader(http.StatusNoContent)
	}
}

func serveUpdateConversationTitle(service ConversationService, audit auditLogger) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		id := request.PathValue("id")
		titleRequest, status, apiError := decodeConversationTitleRequest(response, request)
		if apiError != nil {
			audit.event(request.Context(), "conversation_title_update_rejected",
				slog.String("conversation_id", boundedAuditID(id)),
				slog.Int("status", status), slog.String("code", apiError.Code),
			)
			writeJSONError(response, status, apiError.Code, apiError.Message)
			return
		}
		identity, ok := auth.IdentityFromContext(request.Context())
		if !ok {
			writeUnauthorized(response)
			return
		}
		item, err := service.UpdateTitle(request.Context(), identity, id, titleRequest.Title)
		if err != nil {
			writeConversationError(response, request, audit, "conversation_title_update_rejected", err, id)
			return
		}
		audit.event(request.Context(), "conversation_title_updated", slog.String("conversation_id", boundedAuditID(id)))
		writeJSON(response, http.StatusOK, ConversationEnvelope{Conversation: newConversationResponse(item)})
	}
}

func newConversationResponse(item conversation.Conversation) ConversationResponse {
	return ConversationResponse{
		ID: item.ID, Title: item.Title, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		ExpiresAt: item.ExpiresAt, StoredBytes: item.StoredBytes,
	}
}

func decodeConversationTitleRequest(response http.ResponseWriter, request *http.Request) (ConversationTitleRequest, int, *APIError) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return ConversationTitleRequest{}, http.StatusUnsupportedMediaType, &APIError{Code: "unsupported_media_type", Message: "Content-Type must be application/json"}
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxConversationTitleRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var titleRequest ConversationTitleRequest
	if err := decoder.Decode(&titleRequest); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return ConversationTitleRequest{}, http.StatusRequestEntityTooLarge, &APIError{Code: "request_too_large", Message: "request body is too large"}
		}
		return ConversationTitleRequest{}, http.StatusBadRequest, &APIError{Code: "invalid_request", Message: "request body must be a JSON object containing a title"}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ConversationTitleRequest{}, http.StatusBadRequest, &APIError{Code: "invalid_request", Message: "request body must contain one JSON object"}
	}
	return titleRequest, 0, nil
}

func writeConversationError(response http.ResponseWriter, request *http.Request, audit auditLogger, event string, err error, id string) {
	status, code, message := conversationError(err)
	attributes := []slog.Attr{slog.Int("status", status), slog.String("code", code)}
	if id != "" {
		attributes = append(attributes, slog.String("conversation_id", boundedAuditID(id)))
	}
	audit.event(request.Context(), event, attributes...)
	writeJSONError(response, status, code, message)
}

func conversationError(err error) (int, string, string) {
	switch {
	case errors.Is(err, agent.ErrInvalidEditedArguments):
		return http.StatusBadRequest, "invalid_edited_arguments", "the edited arguments are invalid"
	case errors.Is(err, conversation.ErrConfirmationExpired):
		return http.StatusGone, "confirmation_expired", "the confirmation has expired; the action was not run"
	case errors.Is(err, conversation.ErrNotPending):
		return http.StatusConflict, "confirmation_not_pending", "the turn waits on no such confirmation"
	case errors.Is(err, conversation.ErrInvalidToolResults):
		return http.StatusBadRequest, "invalid_tool_results", "the tool messages must answer exactly the calls of the tools of the client the conversation waits on"
	case errors.Is(err, conversation.ErrMessageTooLarge):
		return http.StatusRequestEntityTooLarge, "history_message_too_large", "a stored message exceeds the display page size"
	case errors.Is(err, conversation.ErrExpired):
		return http.StatusGone, "conversation_expired", "the conversation has expired"
	case errors.Is(err, conversation.ErrNotFound):
		return http.StatusNotFound, "conversation_not_found", "the conversation was not found"
	case errors.Is(err, conversation.ErrBusy):
		return http.StatusConflict, "conversation_busy", "the conversation already has an active turn or an action waiting for a decision"
	case errors.Is(err, conversation.ErrUserLimit):
		return http.StatusConflict, "conversation_quota", "your conversation quota is reached: delete conversations, or wait for them to expire"
	case errors.Is(err, conversation.ErrLimit):
		return http.StatusConflict, "conversation_limit", "the conversation limit has been reached"
	case errors.Is(err, conversation.ErrInvalid):
		return http.StatusBadRequest, "invalid_request", "the conversation request is invalid"
	default:
		return http.StatusInternalServerError, "conversation_failed", "the conversation operation failed"
	}
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
