package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opensvc/ai-agent/internal/auth"
	"github.com/opensvc/ai-agent/internal/conversation"
)

func TestFeedbackRoutes(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	var got conversation.Feedback
	var gotIDs, deletedIDs string
	service := conversationServiceFuncs{
		feedback: func(_ context.Context, identity auth.Identity, id string, turnID string, feedback conversation.Feedback) (conversation.FeedbackRecord, error) {
			if identity.Subject == "" {
				return conversation.FeedbackRecord{}, fmt.Errorf("no identity")
			}
			gotIDs, got = id+"/"+turnID, feedback
			validated, err := feedback.Validate()
			if err != nil {
				return conversation.FeedbackRecord{}, err
			}
			if turnID == "missing" {
				return conversation.FeedbackRecord{}, conversation.ErrNotFound
			}
			if turnID == "full" {
				return conversation.FeedbackRecord{}, conversation.ErrLimit
			}
			return conversation.FeedbackRecord{Feedback: validated, CreatedAt: now, UpdatedAt: now}, nil
		},
		unrate: func(_ context.Context, _ auth.Identity, id string, turnID string) error {
			deletedIDs = id + "/" + turnID
			if turnID == "missing" {
				return conversation.ErrNotFound
			}
			return nil
		},
	}
	handler := newConversationTestHandler(t, service)
	put := func(turnID string, body string, contentType string) *httptest.ResponseRecorder {
		request := authenticatedRequest(http.MethodPut, "/v1/om3/conversations/c-1/turns/"+turnID+"/feedback", body)
		request.Header.Set("Content-Type", contentType)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	response := put("t-1", `{"rating":"down","reasons":["missing_information"],"expected":"the node count"}`, "application/json")
	if response.Code != http.StatusOK || gotIDs != "c-1/t-1" || got.Expected != "the node count" {
		t.Fatalf("put = %d %s, %s %+v", response.Code, response.Body, gotIDs, got)
	}
	var envelope FeedbackResponse
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil || envelope.Feedback.Rating != "down" || envelope.Feedback.Reasons[0] != "missing_information" || !envelope.CreatedAt.Equal(now) {
		t.Fatalf("envelope = %+v, %v", envelope, err)
	}

	for _, test := range []struct {
		turnID, body, contentType string
		status                    int
		code                      string
	}{
		{"t-1", `{"rating":"up","reasons":["not_answered"]}`, "application/json", http.StatusBadRequest, "invalid_feedback"},
		{"t-1", `{"rating":"down","comment":"x"}`, "application/json", http.StatusBadRequest, "invalid_request"},
		{"t-1", `{"rating":"down"}{}`, "application/json", http.StatusBadRequest, "invalid_request"},
		{"t-1", `{"rating":"down"}`, "text/plain", http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{"t-1", `{"rating":"down","expected":"` + strings.Repeat("a", maxFeedbackRequestBytes) + `"}`, "application/json", http.StatusRequestEntityTooLarge, "request_too_large"},
		{"missing", `{"rating":"up"}`, "application/json", http.StatusNotFound, "feedback_target_not_found"},
		{"full", `{"rating":"up"}`, "application/json", http.StatusConflict, "feedback_limit"},
	} {
		response := put(test.turnID, test.body, test.contentType)
		var body ErrorResponse
		if response.Code != test.status || json.NewDecoder(response.Body).Decode(&body) != nil || body.Error.Code != test.code {
			t.Fatalf("%s: status=%d code=%q", test.body[:min(40, len(test.body))], response.Code, body.Error.Code)
		}
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedRequest(http.MethodDelete, "/v1/om3/conversations/c-1/turns/t-1/feedback", ""))
	if response.Code != http.StatusNoContent || deletedIDs != "c-1/t-1" {
		t.Fatalf("delete = %d, %s", response.Code, deletedIDs)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedRequest(http.MethodDelete, "/v1/om3/conversations/c-1/turns/missing/feedback", ""))
	if response.Code != http.StatusNotFound {
		t.Fatalf("delete missing = %d", response.Code)
	}
}
