package conversation

import (
	"encoding/json"
	"errors"
	"fmt"
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

func TestFeedbackContextFitsItsBudget(t *testing.T) {
	names := func(count int) []string {
		out := make([]string, count)
		for i := range out {
			out[i] = fmt.Sprintf("%0128d", i)
		}
		return out
	}
	run := TurnRun{AgentVersion: "v1", Model: "gpt", MCPServer: "om3-mcp", ToolSearch: true}
	small := run
	small.ShownTools = []string{"search_tools", "get_node_status"}
	data, err := newFeedbackContext("om3", []TurnRun{small})
	if err != nil || strings.Contains(string(data), "truncated") || !strings.Contains(string(data), "get_node_status") {
		t.Fatalf("small context = %s, %v", data, err)
	}

	// Two runs of 256 names of 128 bytes exceed the budget: their shown
	// tools are left out and counted.
	big := run
	big.ShownTools = names(256)
	data, err = newFeedbackContext("om3", []TurnRun{big, big})
	if err != nil || len(data) > MaxFeedbackContextBytes {
		t.Fatalf("big context: %d bytes, %v", len(data), err)
	}
	var context FeedbackContext
	if err := json.Unmarshal(data, &context); err != nil {
		t.Fatal(err)
	}
	omitted := context.Runs[0].ShownToolsOmitted + context.Runs[1].ShownToolsOmitted
	kept := len(context.Runs[0].ShownTools) + len(context.Runs[1].ShownTools)
	if !context.Truncated || omitted == 0 || omitted+kept != 512 || context.Runs[0].ShownToolsOmitted != 256 {
		t.Fatalf("context = truncated %v, kept %d, omitted %d", context.Truncated, kept, omitted)
	}

	// The runs of a long turn are left out between the first and the last.
	long := make([]TurnRun, MaxTurnRuns)
	for i := range long {
		long[i] = run
		long[i].AgentVersion = strings.Repeat("v", 200) + fmt.Sprint(i)
		long[i].Model, long[i].MCPVersion, long[i].SystemPromptSHA256 = strings.Repeat("m", 256), strings.Repeat("x", 256), strings.Repeat("a", 64)
	}
	data, err = newFeedbackContext("om3", long)
	if err != nil || len(data) > MaxFeedbackContextBytes {
		t.Fatalf("long context: %d bytes, %v", len(data), err)
	}
	context = FeedbackContext{}
	_ = json.Unmarshal(data, &context)
	if !context.Truncated || context.RunsOmitted == 0 || len(context.Runs)+context.RunsOmitted != MaxTurnRuns ||
		context.Runs[0].AgentVersion != long[0].AgentVersion || context.Runs[len(context.Runs)-1].AgentVersion != long[MaxTurnRuns-1].AgentVersion {
		t.Fatalf("long context kept %d runs, omitted %d", len(context.Runs), context.RunsOmitted)
	}
}
