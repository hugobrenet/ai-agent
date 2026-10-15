package conversation

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/opensvc/ai-agent/internal/llm"
)

func TestFeedbackValidate(t *testing.T) {
	got, err := Feedback{Rating: "down", Reasons: []string{"action_not_done", "not_answered"}, Expected: " line one\nline two "}.Validate()
	if err != nil || strings.Join(got.Reasons, ",") != "not_answered,action_not_done" || got.Expected != "line one\nline two" {
		t.Fatalf("Validate() = %+v, %v", got, err)
	}
	if got, err := (Feedback{Rating: "up"}).Validate(); err != nil || got.Reasons == nil {
		t.Fatalf("up = %+v, %v", got, err)
	}
	for _, feedback := range []Feedback{
		{},
		{Rating: "meh"},
		{Rating: "up", Reasons: []string{"not_answered"}},
		{Rating: "down", Reasons: []string{"unknown"}},
		{Rating: "down", Reasons: []string{"not_answered", "not_answered"}},
		{Rating: "down", Expected: strings.Repeat("é", MaxFeedbackExpectedRunes+1)},
		{Rating: "down", Expected: "bell\a"},
		{Rating: "down", Expected: "\xff"},
	} {
		if _, err := feedback.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v accepted: %v", feedback, err)
		}
	}
	if _, err := (Feedback{Rating: "down", Expected: strings.Repeat("é", MaxFeedbackExpectedRunes)}).Validate(); err != nil {
		t.Fatalf("expected text at the limit refused: %v", err)
	}
}

func TestTurnSnapshotIsBounded(t *testing.T) {
	completed := time.Unix(100, 0).UTC()
	turn := Turn{StartedAt: time.Unix(90, 0).UTC(), CompletedAt: &completed}
	messages := []llm.Message{{Role: llm.RoleUser, Text: strings.Repeat("q", maxSnapshotTextBytes+10)}}
	var calls []llm.ToolCall
	var results []llm.ToolResult
	for i := range maxSnapshotToolCalls + 2 {
		id := string(rune('a' + i))
		calls = append(calls, llm.ToolCall{ID: id, Name: "list_nodes", Arguments: json.RawMessage(`{"name":"` + strings.Repeat("n", maxSnapshotArgumentsBytes) + `"}`)})
		results = append(results, llm.ToolResult{CallID: id, Content: json.RawMessage(`"` + strings.Repeat("r", maxSnapshotResultBytes) + `"`), IsError: i == 0})
	}
	messages = append(messages,
		llm.Message{Role: llm.RoleAssistant, Text: "looking", ToolCalls: calls},
		llm.Message{Role: llm.RoleTool, ToolResults: results},
		llm.Message{Role: llm.RoleAssistant, Text: "done"},
	)
	snapshot := NewTurnSnapshot(turn, messages)
	if !snapshot.Truncated || len(snapshot.ToolCalls) != maxSnapshotToolCalls || snapshot.ToolCallsOmitted != 2 ||
		len(snapshot.Question) > maxSnapshotTextBytes+len("…") || snapshot.Answer != "looking\n\ndone" ||
		!snapshot.ToolCalls[0].IsError || snapshot.ToolCalls[1].IsError || snapshot.ToolCalls[0].ResultBytes != maxSnapshotResultBytes+2 ||
		len(snapshot.ToolCalls[0].Arguments) > maxSnapshotArgumentsBytes+len("…") || len(snapshot.ToolCalls[0].Result) > maxSnapshotResultBytes+len("…") ||
		!snapshot.StartedAt.Equal(turn.StartedAt) || !snapshot.CompletedAt.Equal(completed) {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) > 48<<10 {
		t.Fatalf("snapshot is %d bytes, %v", len(encoded), err)
	}
	if got := NewTurnSnapshot(turn, []llm.Message{{Role: llm.RoleUser, Text: "é" + strings.Repeat("x", maxSnapshotTextBytes)}}); !strings.HasSuffix(got.Question, "…") || !json.Valid([]byte(`"`+got.Question+`"`)) {
		t.Fatalf("cut question = %q", got.Question)
	}
}
