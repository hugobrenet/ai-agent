package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opensvc/ai-agent/internal/conversation"
	conversationsqlite "github.com/opensvc/ai-agent/internal/conversation/sqlite"
	"github.com/opensvc/ai-agent/internal/llm"
)

func TestFeedbackExportLeavesOutIdentityAndToolResults(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "agent")
	path := filepath.Join(directory, "conversations.db")
	t.Setenv("OPENSVC_AI_CONVERSATION_DB_PATH", path)
	store, err := conversationsqlite.Open(t.Context(), conversationsqlite.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	owner := conversation.Owner{Backend: "oc3", Issuer: "https://sso.example.com/", Subject: "alice"}
	now := time.Now().UTC()
	if err := store.CreateConversation(t.Context(), conversation.Conversation{ID: "conversation-1", Owner: owner, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginTurn(t.Context(), owner, "conversation-1", "turn-1", now); err != nil {
		t.Fatal(err)
	}
	messages := []llm.Message{
		{Role: llm.RoleUser, Text: "how many nodes"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "count_nodes", Arguments: json.RawMessage(`{}`)}}},
		{Role: llm.RoleTool, ToolResults: []llm.ToolResult{{CallID: "call-1", Content: json.RawMessage(`{"total":484}`)}}},
		{Role: llm.RoleAssistant, Text: "484 nodes"},
	}
	if err := store.CompleteTurn(t.Context(), owner, "conversation-1", "turn-1", now, now.Add(time.Hour), messages, conversation.TurnRun{}); err != nil {
		t.Fatal(err)
	}
	turn, stored, err := store.TurnMessages(t.Context(), owner, "conversation-1", "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := json.Marshal(conversation.NewTurnSnapshot(turn, stored))
	if _, err := store.PutFeedback(t.Context(), owner, "conversation-1", conversation.FeedbackRecord{
		ID: "feedback-1", TurnID: "turn-1", Feedback: conversation.Feedback{Rating: "down", Reasons: []string{"missing_information"}, Expected: "per OS"},
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), Snapshot: snapshot, Context: json.RawMessage(`{"backend":"oc3"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runFeedback([]string{"export", "-since", "1h"}, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, secret := range []string{"alice", "sso.example.com", "conversation-1", "turn-1", "484}"} {
		if strings.Contains(text, secret) {
			t.Fatalf("export holds %q: %s", secret, text)
		}
	}
	var line feedbackExport
	if err := json.Unmarshal(output.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line.Rating != "down" || line.Expected != "per OS" || line.Snapshot.Question != "how many nodes" || line.Snapshot.Answer != "484 nodes" ||
		len(line.Snapshot.ToolCalls) != 1 || line.Snapshot.ToolCalls[0].Result != "" || line.Snapshot.ToolCalls[0].ResultBytes != 13 {
		t.Fatalf("line = %+v", line)
	}

	output.Reset()
	if err := runFeedback([]string{"export", "-with-tool-results"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `{\"total\":484}`) {
		t.Fatalf("export with results = %s", output.String())
	}
	for _, args := range [][]string{nil, {"import"}, {"export", "-since", "0s"}, {"export", "extra"}} {
		if err := runFeedback(args, &output); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
}
