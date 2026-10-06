package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/opensvc/ai-agent/internal/conversation"
	"github.com/opensvc/ai-agent/internal/llm"
)

func completeDisplayTurn(t *testing.T, store *Store, id, turnID string, at time.Time, messages []llm.Message) {
	t.Helper()
	if _, err := store.BeginTurn(t.Context(), testOwner, id, turnID, at); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteTurn(t.Context(), testOwner, id, turnID, at.Add(time.Second), at.Add(time.Hour), messages); err != nil {
		t.Fatal(err)
	}
}

func TestDisplayMessagesPaginateCompletedTextWithoutToolPayloads(t *testing.T) {
	store, path := openTestStore(t, Config{})
	item := testConversation("conversation-1", testOwner, testNow)
	if err := store.CreateConversation(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	completeDisplayTurn(t, store, item.ID, "turn-1", testNow, toolTranscript())
	if _, err := store.BeginTurn(t.Context(), testOwner, item.ID, "turn-failed", testNow.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.FailTurn(t.Context(), testOwner, item.ID, "turn-failed", conversation.TurnCanceled, "request_canceled", testNow.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	completeDisplayTurn(t, store, item.ID, "turn-3", testNow.Add(4*time.Second), []llm.Message{
		{Role: llm.RoleUser, Text: "second prompt"}, {Role: llm.RoleAssistant, Text: "second answer"},
	})
	// A running turn must neither block reads nor appear in the transcript.
	if _, err := store.BeginTurn(t.Context(), testOwner, item.ID, "turn-running", testNow.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListMessages(t.Context(), testOwner, item.ID, conversation.MessageQuery{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 3 || page.NextCursor != "1:4" {
		t.Fatalf("latest page = %+v", page)
	}
	for index, want := range []string{"cluster healthy", "second prompt", "second answer"} {
		if page.Messages[index].Text != want {
			t.Fatalf("message %d = %+v", index, page.Messages[index])
		}
	}
	if page.Messages[0].ID != "turn-1:4" || !page.Messages[1].CreatedAt.Equal(testNow.Add(4*time.Second)) || !page.Messages[2].CreatedAt.Equal(testNow.Add(5*time.Second)) {
		t.Fatalf("message metadata = %+v", page.Messages)
	}
	older, err := store.ListMessages(t.Context(), testOwner, item.ID, conversation.MessageQuery{Limit: 3, Before: page.NextCursor})
	if err != nil || len(older.Messages) != 1 || older.Messages[0].Text != "health" || older.NextCursor != "" {
		t.Fatalf("older page = %+v, %v", older, err)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"tool_calls", "tool_results", "arguments", "get_cluster_health", "request_canceled"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("display exposes %s: %s", forbidden, encoded)
		}
	}
	// Existing persisted turns remain readable after reopening, without a schema change.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	again, err := reopened.ListMessages(t.Context(), testOwner, item.ID, conversation.MessageQuery{Limit: 3})
	if err != nil || !reflect.DeepEqual(again, page) {
		t.Fatalf("reopened page = %+v, %v", again, err)
	}
}

func TestDisplayMessagesAreBoundToOwner(t *testing.T) {
	store, _ := openTestStore(t, Config{})
	item := testConversation("conversation-1", testOwner, testNow)
	if err := store.CreateConversation(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	completeDisplayTurn(t, store, item.ID, "turn-1", testNow, toolTranscript())
	for _, owner := range []conversation.Owner{
		{ClusterID: "other-cluster", Issuer: testOwner.Issuer, Subject: testOwner.Subject},
		{ClusterID: testOwner.ClusterID, Issuer: "other-issuer", Subject: testOwner.Subject},
		{ClusterID: testOwner.ClusterID, Issuer: testOwner.Issuer, Subject: "other-user"},
	} {
		page, err := store.ListMessages(t.Context(), owner, item.ID, conversation.MessageQuery{Before: "2:1"})
		if !errors.Is(err, conversation.ErrNotFound) || len(page.Messages) != 0 {
			t.Fatalf("foreign owner got %+v, %v", page, err)
		}
	}
	if _, err := store.ListMessages(t.Context(), testOwner, "missing", conversation.MessageQuery{}); !errors.Is(err, conversation.ErrNotFound) {
		t.Fatalf("missing messages error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.ListMessages(ctx, testOwner, item.ID, conversation.MessageQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled messages error = %v", err)
	}
}

func TestDisplayMessagesEmptyPageAndStableCursor(t *testing.T) {
	store, _ := openTestStore(t, Config{})
	item := testConversation("conversation-1", testOwner, testNow)
	if err := store.CreateConversation(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListMessages(t.Context(), testOwner, item.ID, conversation.MessageQuery{})
	if err != nil || page.Messages == nil || len(page.Messages) != 0 || page.NextCursor != "" {
		t.Fatalf("empty page = %+v, %v", page, err)
	}
	completeDisplayTurn(t, store, item.ID, "turn-1", testNow, toolTranscript())
	page, err = store.ListMessages(t.Context(), testOwner, item.ID, conversation.MessageQuery{Limit: 1})
	if err != nil || len(page.Messages) != 1 || page.NextCursor != "1:4" {
		t.Fatalf("latest message = %+v, %v", page, err)
	}
	completeDisplayTurn(t, store, item.ID, "turn-2", testNow.Add(2*time.Second), toolTranscript())
	older, err := store.ListMessages(t.Context(), testOwner, item.ID, conversation.MessageQuery{Limit: 1, Before: page.NextCursor})
	if err != nil || len(older.Messages) != 1 || older.Messages[0].ID != "turn-1:1" || older.NextCursor != "" {
		t.Fatalf("cursor shifted after new turn = %+v, %v", older, err)
	}
}

func TestDisplayMessagePageIsBoundedByEncodedBytes(t *testing.T) {
	store, _ := openTestStore(t, Config{})
	item := testConversation("conversation-1", testOwner, testNow)
	if err := store.CreateConversation(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	// '<' is escaped to six bytes by encoding/json. The page fits one answer,
	// not two, despite the raw text being far below the byte budget.
	text := strings.Repeat("<", conversation.MaxMessagePageBytes/8)
	for index, id := range []string{"turn-1", "turn-2"} {
		completeDisplayTurn(t, store, item.ID, id, testNow.Add(time.Duration(index)*2*time.Second), []llm.Message{
			{Role: llm.RoleUser, Text: "hello"}, {Role: llm.RoleAssistant, Text: text},
		})
	}
	page, err := store.ListMessages(t.Context(), testOwner, item.ID, conversation.MessageQuery{})
	if err != nil || len(page.Messages) != 2 || page.NextCursor != "2:1" {
		t.Fatalf("byte-bounded page length = %d, cursor = %q, error = %v", len(page.Messages), page.NextCursor, err)
	}
	encoded, err := json.Marshal(page)
	if err != nil || len(encoded) > conversation.MaxMessagePageBytes {
		t.Fatalf("page bytes = %d, error = %v", len(encoded), err)
	}
	older, err := store.ListMessages(t.Context(), testOwner, item.ID, conversation.MessageQuery{Before: page.NextCursor})
	if err != nil || len(older.Messages) != 2 || older.NextCursor != "" {
		t.Fatalf("older byte-bounded page length = %d, error = %v", len(older.Messages), err)
	}
}

func TestDisplayMessagesRejectOversizedSingleMessageWithoutTruncation(t *testing.T) {
	for _, text := range []string{
		strings.Repeat("x", conversation.MaxMessagePageBytes+1),
		strings.Repeat("<", conversation.MaxMessagePageBytes/6+1),
	} {
		store, _ := openTestStore(t, Config{})
		item := testConversation("conversation-1", testOwner, testNow)
		if err := store.CreateConversation(t.Context(), item); err != nil {
			t.Fatal(err)
		}
		completeDisplayTurn(t, store, item.ID, "turn-1", testNow, []llm.Message{
			{Role: llm.RoleUser, Text: "hello"}, {Role: llm.RoleAssistant, Text: text},
		})
		if _, err := store.ListMessages(t.Context(), testOwner, item.ID, conversation.MessageQuery{}); !errors.Is(err, conversation.ErrMessageTooLarge) {
			t.Fatalf("oversized display error = %v", err)
		}
	}
}
