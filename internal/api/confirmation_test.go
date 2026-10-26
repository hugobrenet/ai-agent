package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opensvc/ai-agent/internal/agent"
	"github.com/opensvc/ai-agent/internal/auth"
	"github.com/opensvc/ai-agent/internal/conversation"
)

var confirmationExpiry = time.Date(2026, 10, 9, 17, 45, 0, 0, time.UTC)

// The interrupts of the runs and their resume are tested with the runs
// (agui_test.go, agui_golden_test.go): the conversation read gives the
// confirmation again after a reload, and a resume refused by the
// conversation answers the code of its refusal.
func TestRunResumeReturnsStableErrors(t *testing.T) {
	pending := func(context.Context, auth.Identity, string) (*conversation.PendingConfirmation, error) {
		return &conversation.PendingConfirmation{TurnID: "turn-id", ConfirmationID: "confirmation-id", State: []byte(`{"calls":[{"ID":"call-1","Name":"stop_object"}]}`)}, nil
	}
	for _, test := range []struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		{conversation.ErrNotPending, http.StatusConflict, "confirmation_not_pending"},
		{conversation.ErrConfirmationExpired, http.StatusGone, "confirmation_expired"},
		{conversation.ErrNotFound, http.StatusNotFound, "conversation_not_found"},
	} {
		service := conversationServiceFuncs{pending: pending, confirm: func(context.Context, auth.Identity, string, string, string, agent.Decision) (conversation.TurnExecution, error) {
			return nil, test.err
		}}
		response := runRequest(t, service, `{"threadId":"conversation-id","runId":"run-1","messages":[],"resume":[{"interruptId":"confirmation-id","status":"resolved","payload":{"approved":true}}]}`)
		if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) {
			t.Fatalf("%v: got %d %s", test.err, response.Code, response.Body.String())
		}
	}
}

func TestConversationReadReturnsThePendingConfirmation(t *testing.T) {
	service := conversationServiceFuncs{
		get: func(context.Context, auth.Identity, string) (conversation.Conversation, error) {
			return conversation.Conversation{ID: "conversation-id"}, nil
		},
		pending: func(context.Context, auth.Identity, string) (*conversation.PendingConfirmation, error) {
			return &conversation.PendingConfirmation{
				TurnID: "turn-id", ConfirmationID: "confirmation-id", ToolName: "stop_object", ToolTitle: "Stop object",
				Arguments: json.RawMessage(`{"path":"prod/svc/web"}`), ExpiresAt: confirmationExpiry,
				State: []byte(`{"argument_types":{"yaml":"application/yaml"},"input_schema":{"type":"object","properties":{"path":{"type":"string"}}},"preview":{"summary":{"key":"stop_object"}},"preview_arguments":{"path":"prod/svc/api"}}`),
			}, nil
		},
	}
	response := httptest.NewRecorder()
	newConversationTestHandler(t, service).ServeHTTP(response, authenticatedRequest(http.MethodGet, "/v1/om3/conversations/conversation-id", ""))
	want := `"pending_confirmation":{"id":"confirmation-id","turn_id":"turn-id","tool":{"name":"stop_object","title":"Stop object","destructive":true},"arguments":{"path":"prod/svc/web"},"argument_types":{"yaml":"application/yaml"},"editable":true,"input_schema":{"type":"object","properties":{"path":{"type":"string"}}},"expires_at":"2026-10-09T17:45:00Z","preview":{"summary":{"key":"stop_object"}},"preview_arguments":{"path":"prod/svc/api"}}`
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), want) {
		t.Fatalf("got %d %s", response.Code, response.Body.String())
	}
}
