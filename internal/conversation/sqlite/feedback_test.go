package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/opensvc/ai-agent/internal/agent"
	"github.com/opensvc/ai-agent/internal/auth"
	"github.com/opensvc/ai-agent/internal/conversation"
	"github.com/opensvc/ai-agent/internal/llm"
)

func testFeedback(id string, turnID string, feedback conversation.Feedback, at time.Time) conversation.FeedbackRecord {
	return conversation.FeedbackRecord{
		ID: id, TurnID: turnID, Feedback: feedback, CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(time.Hour),
		Snapshot: json.RawMessage(`{"question":"health"}`), Context: json.RawMessage(`{"backend":"om3"}`),
	}
}

func TestFeedbackIsSetReplacedListedAndDeleted(t *testing.T) {
	store, _ := openTestStore(t, Config{})
	item := testConversation("conversation-1", testOwner, testNow)
	if err := store.CreateConversation(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	completeDisplayTurn(t, store, item.ID, "turn-1", testNow, toolTranscript())

	turn, messages, err := store.TurnMessages(t.Context(), testOwner, item.ID, "turn-1")
	if err != nil || turn.Status != conversation.TurnCompleted || len(messages) != 4 || messages[2].ToolResults[0].CallID != "call-1" {
		t.Fatalf("turn messages = %+v, %+v, %v", turn, messages, err)
	}

	down := conversation.Feedback{Rating: "down", Reasons: []string{"incorrect_or_outdated", "not_answered"}, Expected: "the node count"}
	stored, err := store.PutFeedback(t.Context(), testOwner, item.ID, testFeedback("feedback-1", "turn-1", down, testNow))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Feedback.Reasons[0] != "not_answered" || stored.Feedback.Expected != "the node count" {
		t.Fatalf("stored = %+v", stored)
	}
	page, err := store.ListMessages(t.Context(), testOwner, item.ID, conversation.MessageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Messages[0].Feedback != nil || page.Messages[1].Feedback == nil || page.Messages[1].Feedback.Rating != "down" {
		t.Fatalf("messages = %+v", page.Messages)
	}

	// A new feedback replaces the opinion and keeps the first snapshot.
	replacement := testFeedback("feedback-2", "turn-1", conversation.Feedback{Rating: "up"}, testNow.Add(time.Minute))
	replacement.Snapshot = json.RawMessage(`{"question":"other"}`)
	stored, err = store.PutFeedback(t.Context(), testOwner, item.ID, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != "feedback-1" || stored.Feedback.Rating != "up" || len(stored.Feedback.Reasons) != 0 || string(stored.Snapshot) != `{"question":"health"}` ||
		!stored.CreatedAt.Equal(testNow) || !stored.UpdatedAt.Equal(testNow.Add(time.Minute)) {
		t.Fatalf("replaced = %+v", stored)
	}

	if err := store.DeleteFeedback(t.Context(), testOwner, item.ID, "turn-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteFeedback(t.Context(), testOwner, item.ID, "turn-1"); !errors.Is(err, conversation.ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

func TestFeedbackIsBoundToOwnerAndCompletedTurns(t *testing.T) {
	store, _ := openTestStore(t, Config{})
	item := testConversation("conversation-1", testOwner, testNow)
	if err := store.CreateConversation(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	completeDisplayTurn(t, store, item.ID, "turn-1", testNow, toolTranscript())
	if _, err := store.BeginTurn(t.Context(), testOwner, item.ID, "turn-running", testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	other := testOwner
	other.Subject = "mallory"
	up := conversation.Feedback{Rating: "up"}
	if _, err := store.PutFeedback(t.Context(), other, item.ID, testFeedback("f", "turn-1", up, testNow)); !errors.Is(err, conversation.ErrNotFound) {
		t.Fatalf("other owner = %v", err)
	}
	if _, _, err := store.TurnMessages(t.Context(), other, item.ID, "turn-1"); !errors.Is(err, conversation.ErrNotFound) {
		t.Fatalf("other owner messages = %v", err)
	}
	if _, err := store.PutFeedback(t.Context(), testOwner, item.ID, testFeedback("f", "turn-running", up, testNow)); !errors.Is(err, conversation.ErrInvalid) {
		t.Fatalf("running turn = %v", err)
	}
	if _, err := store.PutFeedback(t.Context(), testOwner, item.ID, testFeedback("f", "turn-unknown", up, testNow)); !errors.Is(err, conversation.ErrNotFound) {
		t.Fatalf("unknown turn = %v", err)
	}
	if _, err := store.PutFeedback(t.Context(), testOwner, item.ID, testFeedback("f", "turn-1", conversation.Feedback{Rating: "up", Reasons: []string{"not_answered"}}, testNow)); !errors.Is(err, conversation.ErrInvalid) {
		t.Fatalf("reasons with up = %v", err)
	}
	if err := store.DeleteFeedback(t.Context(), other, item.ID, "turn-1"); !errors.Is(err, conversation.ErrNotFound) {
		t.Fatalf("other owner delete = %v", err)
	}
}

func TestFeedbackOutlivesItsConversationAnonymously(t *testing.T) {
	store, _ := openTestStore(t, Config{})
	item := testConversation("conversation-1", testOwner, testNow)
	if err := store.CreateConversation(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	completeDisplayTurn(t, store, item.ID, "turn-1", testNow, toolTranscript())
	if _, err := store.PutFeedback(t.Context(), testOwner, item.ID, testFeedback("feedback-1", "turn-1", conversation.Feedback{Rating: "down", Expected: "more"}, testNow)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteConversation(t.Context(), testOwner, item.ID); err != nil {
		t.Fatal(err)
	}
	var turnID *string
	if err := store.db.QueryRowContext(t.Context(), "SELECT turn_id FROM feedback WHERE id = 'feedback-1'").Scan(&turnID); err != nil || turnID != nil {
		t.Fatalf("turn_id = %v, %v", turnID, err)
	}
	var exported []conversation.FeedbackRecord
	if err := store.ExportFeedback(t.Context(), testNow.Add(-time.Hour), func(record conversation.FeedbackRecord) error {
		exported = append(exported, record)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(exported) != 1 || exported[0].TurnID != "" || exported[0].Feedback.Expected != "more" || string(exported[0].Snapshot) != `{"question":"health"}` {
		t.Fatalf("exported = %+v", exported)
	}
	if err := store.ExportFeedback(t.Context(), testNow.Add(time.Second), func(conversation.FeedbackRecord) error { return errors.New("unexpected") }); err != nil {
		t.Fatalf("export since later = %v", err)
	}
	if deleted, err := store.DeleteExpiredFeedback(t.Context(), testNow.Add(59*time.Minute), 10); err != nil || deleted != 0 {
		t.Fatalf("early expiry = %d, %v", deleted, err)
	}
	if deleted, err := store.DeleteExpiredFeedback(t.Context(), testNow.Add(time.Hour), 10); err != nil || deleted != 1 {
		t.Fatalf("expiry = %d, %v", deleted, err)
	}
}

func TestFeedbackLimits(t *testing.T) {
	store, _ := openTestStore(t, Config{MaxFeedback: 1})
	item := testConversation("conversation-1", testOwner, testNow)
	if err := store.CreateConversation(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	completeDisplayTurn(t, store, item.ID, "turn-1", testNow, toolTranscript())
	completeDisplayTurn(t, store, item.ID, "turn-2", testNow.Add(time.Minute), toolTranscript())
	up := conversation.Feedback{Rating: "up"}
	if _, err := store.PutFeedback(t.Context(), testOwner, item.ID, testFeedback("f1", "turn-1", up, testNow)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutFeedback(t.Context(), testOwner, item.ID, testFeedback("f2", "turn-2", up, testNow)); !errors.Is(err, conversation.ErrLimit) {
		t.Fatalf("over the limit = %v", err)
	}
	// Replacing a feedback is never refused by the limit.
	if _, err := store.PutFeedback(t.Context(), testOwner, item.ID, testFeedback("f3", "turn-1", conversation.Feedback{Rating: "down"}, testNow)); err != nil {
		t.Fatal(err)
	}
	large := testFeedback("f4", "turn-1", up, testNow)
	large.Snapshot = json.RawMessage(`"` + strings.Repeat("a", maxFeedbackRecordBytes) + `"`)
	if _, err := store.PutFeedback(t.Context(), testOwner, item.ID, large); !errors.Is(err, conversation.ErrLimit) {
		t.Fatalf("oversized record = %v", err)
	}
}

type unusedRunner struct{}

func (unusedRunner) RunTurn(context.Context, []llm.Message, string, agent.EmitFunc) (agent.TurnResult, error) {
	return agent.TurnResult{}, errors.New("unexpected turn")
}

func (unusedRunner) ResumeTurn(context.Context, []llm.Message, agent.SuspendedTurn, agent.Decision, agent.EmitFunc) (agent.TurnResult, error) {
	return agent.TurnResult{}, errors.New("unexpected turn")
}

func TestServiceFeedbackSnapshotsTheTurn(t *testing.T) {
	store, _ := openTestStore(t, Config{})
	service, err := conversation.NewService(store, unusedRunner{}, conversation.ServiceConfig{
		Backend: "om3", FeedbackRetention: 48 * time.Hour,
		FeedbackContext: conversation.NewFeedbackContext("om3", "v1.2.3", "gpt-test", "You are a test assistant."),
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := auth.Identity{ClusterID: testOwner.ClusterID, Issuer: testOwner.Issuer, Subject: testOwner.Subject}
	item, err := service.Create(t.Context(), identity)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := store.BeginTurn(t.Context(), testOwner, item.ID, "turn-1", now); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("v", 300)
	if err := store.CompleteTurn(t.Context(), testOwner, item.ID, "turn-1", now.Add(time.Second), now.Add(time.Hour), toolTranscript(),
		conversation.TurnAgent{MCPServer: "oc3-mcp", MCPVersion: long, ToolsSHA256: strings.Repeat("ab", 32)}); err != nil {
		t.Fatal(err)
	}

	record, err := service.SetFeedback(t.Context(), identity, item.ID, "turn-1", conversation.Feedback{Rating: "down", Reasons: []string{"missing_information"}, Expected: "  the nodes  "})
	if err != nil {
		t.Fatal(err)
	}
	if record.Feedback.Expected != "the nodes" || !record.ExpiresAt.Equal(record.CreatedAt.Add(48*time.Hour)) {
		t.Fatalf("record = %+v", record)
	}
	var snapshot conversation.TurnSnapshot
	if err := json.Unmarshal(record.Snapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Question != "health" || snapshot.Answer != "cluster healthy" || len(snapshot.ToolCalls) != 1 ||
		snapshot.ToolCalls[0].Name != "get_cluster_health" || snapshot.ToolCalls[0].Result != `{"status":"healthy"}` || snapshot.ToolCalls[0].ResultBytes != 20 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	var agentContext conversation.FeedbackContext
	if err := json.Unmarshal(record.Context, &agentContext); err != nil {
		t.Fatal(err)
	}
	if agentContext.Backend != "om3" || agentContext.AgentVersion != "v1.2.3" || agentContext.Model != "gpt-test" || len(agentContext.SystemPromptSHA256) != 64 ||
		agentContext.MCPServer != "oc3-mcp" || agentContext.MCPVersion != long[:256] || agentContext.ToolsSHA256 != strings.Repeat("ab", 32) {
		t.Fatalf("context = %+v", agentContext)
	}

	other := auth.Identity{ClusterID: testOwner.ClusterID, Issuer: testOwner.Issuer, Subject: "mallory"}
	if _, err := service.SetFeedback(t.Context(), other, item.ID, "turn-1", conversation.Feedback{Rating: "up"}); !errors.Is(err, conversation.ErrNotFound) {
		t.Fatalf("other identity = %v", err)
	}
	if err := service.DeleteFeedback(t.Context(), other, item.ID, "turn-1"); !errors.Is(err, conversation.ErrNotFound) {
		t.Fatalf("other identity delete = %v", err)
	}
	if err := service.DeleteFeedback(t.Context(), identity, item.ID, "turn-1"); err != nil {
		t.Fatal(err)
	}
}
