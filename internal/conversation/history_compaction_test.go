package conversation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/opensvc/ai-agent/internal/llm"
)

// structuredResult is a tool result as the agent stores it.
func structuredResult(t *testing.T, structured string, withText bool) json.RawMessage {
	t.Helper()
	content := "[]"
	if withText {
		quoted, _ := json.Marshal(structured)
		content = `[{"type":"text","text":` + string(quoted) + `}]`
	}
	raw := json.RawMessage(`{"content":` + content + `,"structuredContent":` + structured + `}`)
	if !json.Valid(raw) {
		t.Fatalf("invalid result %s", raw)
	}
	return raw
}

func disks(count int) string {
	items := make([]string, count)
	for i := range items {
		items[i] = fmt.Sprintf(`{"id":"vd%c","devpath":"/dev/vd%c","size_bytes":12345678901234567%03d,"type":"disk"}`, 'a'+i%26, 'a'+i%26, i)
	}
	return `{"node":"dev5n1","count":` + fmt.Sprint(count) + `,"disks":[` + strings.Join(items, ",") + `],` +
		`"provenance":{"source":"opensvc_daemon","observed_at":"2026-10-15T13:00:00Z"}}`
}

func turnWith(prompt string, results ...llm.ToolResult) []llm.Message {
	calls := make([]llm.ToolCall, len(results))
	for i, result := range results {
		calls[i] = llm.ToolCall{ID: result.CallID, Name: "list_node_disks", Arguments: json.RawMessage(`{"node":"dev5n1"}`)}
	}
	return []llm.Message{
		{Role: llm.RoleUser, Text: prompt},
		{Role: llm.RoleAssistant, ToolCalls: calls},
		{Role: llm.RoleTool, ToolResults: results},
		{Role: llm.RoleAssistant, Text: "answer"},
	}
}

type decodedMarker struct {
	Policy           string     `json:"policy"`
	Partial          bool       `json:"partial"`
	OriginalBytes    int        `json:"original_bytes"`
	Omissions        []omission `json:"omissions"`
	OmissionsOmitted int        `json:"omissions_omitted"`
}

func decodeCompacted(t *testing.T, raw json.RawMessage) (map[string]any, decodedMarker) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var envelope map[string]any
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("compacted result is not JSON: %v: %s", err, raw)
	}
	var marker decodedMarker
	data, _ := json.Marshal(envelope["_history_compaction"])
	_ = json.Unmarshal(data, &marker)
	structured, _ := envelope["structuredContent"].(map[string]any)
	return structured, marker
}

func TestCompactHistoryKeepsSmallErrorAndUnsupportedResults(t *testing.T) {
	small := structuredResult(t, `{"node":"dev5n1","status":"up"}`, true)
	failed := json.RawMessage(`{"content":[{"type":"text","text":"` + strings.Repeat("e", 4000) + `"}]}`)
	text := json.RawMessage(`{"content":[{"type":"text","text":"` + strings.Repeat("t", 4000) + `"}]}`)
	image := json.RawMessage(`{"content":[{"type":"image","data":"` + strings.Repeat("A", 4000) + `","mimeType":"image/png"}],"structuredContent":{"a":1}}`)
	history := turnWith("q", llm.ToolResult{CallID: "a", Content: small}, llm.ToolResult{CallID: "b", Content: failed, IsError: true},
		llm.ToolResult{CallID: "c", Content: text}, llm.ToolResult{CallID: "d", Content: image})
	compacted, stats := compactHistory(history, HistoryCompaction{})
	if !reflect.DeepEqual(compacted, history) || stats.CompactedResults != 0 || stats.RemovedTurns != 0 {
		t.Fatalf("results changed: %+v", stats)
	}
}

func TestCompactHistoryCompactsAStructuredResult(t *testing.T) {
	original := structuredResult(t, disks(24), true)
	history := turnWith("q", llm.ToolResult{CallID: "call-1", Content: original})
	before, _ := json.Marshal(history)
	compacted, stats := compactHistory(history, HistoryCompaction{})
	if after, _ := json.Marshal(history); !bytes.Equal(before, after) {
		t.Fatal("the stored history was changed")
	}
	result := compacted[2].ToolResults[0]
	if result.CallID != "call-1" || compacted[1].ToolCalls[0].ID != "call-1" || stats.CompactedResults != 1 {
		t.Fatalf("calls and results are not paired: %+v", compacted)
	}
	if len(result.Content) > DefaultHistoryCompaction.MaxResultBytes {
		t.Fatalf("compacted result holds %d bytes", len(result.Content))
	}
	structured, marker := decodeCompacted(t, result.Content)
	if marker.Policy != "v1" || !marker.Partial || marker.OriginalBytes != len(original) {
		t.Fatalf("marker = %+v", marker)
	}
	// The scalars, the count as the tool returned it, and the nested
	// provenance are kept; the disks array holds real disks only.
	if structured["node"] != "dev5n1" || structured["count"] != json.Number("24") {
		t.Fatalf("scalars = %v", structured)
	}
	if provenance, _ := structured["provenance"].(map[string]any); provenance["observed_at"] != "2026-10-15T13:00:00Z" {
		t.Fatalf("provenance = %v", structured["provenance"])
	}
	kept, _ := structured["disks"].([]any)
	for _, disk := range kept {
		if _, ok := disk.(map[string]any)["id"]; !ok {
			t.Fatalf("an artificial item was added: %v", disk)
		}
	}
	// Integers are kept as written, never through float64.
	if first := kept[0].(map[string]any)["size_bytes"]; first != json.Number("12345678901234567000") {
		t.Fatalf("size_bytes = %v", first)
	}
	var tail *omission
	for i, o := range marker.Omissions {
		if o.Path == "/structuredContent/disks" && o.Kind == omittedArrayTail {
			tail = &marker.Omissions[i]
		}
	}
	if tail == nil || tail.Kept != len(kept) || tail.Kept+tail.Omitted != 24 {
		t.Fatalf("omissions = %+v, kept %d", marker.Omissions, len(kept))
	}
	if marker.Omissions[0] != (omission{Path: "/content", Kind: omittedContent, Omitted: 1}) {
		t.Fatalf("the text copy is not reported: %+v", marker.Omissions)
	}
}

func TestCompactHistoryOmitsLongStringsWhole(t *testing.T) {
	long := strings.Repeat("x", 300)
	structured := `{"id":"node-1","description":"` + long + `","items":[` + strings.TrimSuffix(strings.Repeat(`{"n":"`+strings.Repeat("y", 100)+`"},`, 40), ",") + `]}`
	compacted, _ := compactHistory(turnWith("q", llm.ToolResult{CallID: "c", Content: structuredResult(t, structured, false)}), HistoryCompaction{})
	value, marker := decodeCompacted(t, compacted[2].ToolResults[0].Content)
	if _, ok := value["description"]; ok || value["id"] != "node-1" {
		t.Fatalf("value = %v", value)
	}
	found := false
	for _, o := range marker.Omissions {
		found = found || (o.Path == "/structuredContent/description" && o.Kind == omittedLongString && o.Omitted == 300)
	}
	if !found {
		t.Fatalf("omissions = %+v", marker.Omissions)
	}
}

func TestCompactHistoryBoundsTheOmissions(t *testing.T) {
	// 8 groups of 3 short lists, each longer than kept: 24 omissions in a
	// small result.
	list := "[" + strings.TrimSuffix(strings.Repeat("1,", 60), ",") + "]"
	var fields []string
	for i := range 8 {
		fields = append(fields, fmt.Sprintf(`"g%d":{"a":%s,"b":%s,"c":%s}`, i, list, list, list))
	}
	compacted, _ := compactHistory(turnWith("q", llm.ToolResult{CallID: "c", Content: structuredResult(t, "{"+strings.Join(fields, ",")+"}", false)}), HistoryCompaction{})
	result := compacted[2].ToolResults[0].Content
	_, marker := decodeCompacted(t, result)
	if len(result) > DefaultHistoryCompaction.MaxResultBytes || len(marker.Omissions) > DefaultHistoryCompaction.MaxOmissions || marker.OmissionsOmitted == 0 {
		t.Fatalf("%d bytes, %d omissions, %d omitted", len(result), len(marker.Omissions), marker.OmissionsOmitted)
	}
}

func TestCompactHistoryKeepsTheGlobalBudget(t *testing.T) {
	var history []llm.Message
	for turn := range 6 {
		var results []llm.ToolResult
		for i := range 4 {
			results = append(results, llm.ToolResult{CallID: fmt.Sprintf("t%d-%d", turn, i), Content: structuredResult(t, disks(24), true)})
		}
		history = append(history, turnWith(fmt.Sprintf("question %d", turn), results...)...)
	}
	compacted, stats := compactHistory(history, HistoryCompaction{})
	size := func(messages []llm.Message) (total int, perTurn []int) {
		for _, message := range messages {
			if message.Role == llm.RoleUser {
				perTurn = append(perTurn, 0)
			}
			for _, result := range message.ToolResults {
				total += len(result.Content)
				perTurn[len(perTurn)-1] += len(result.Content)
			}
		}
		return total, perTurn
	}
	total, perTurn := size(compacted)
	if total > DefaultHistoryCompaction.MaxHistoryResultBytes || compacted[0].Role != llm.RoleUser {
		t.Fatalf("history results hold %d bytes, first role %s", total, compacted[0].Role)
	}
	// The oldest turns are reduced first: the last turn keeps the most.
	if perTurn[len(perTurn)-1] < perTurn[0] {
		t.Fatalf("per turn = %v", perTurn)
	}
	if stats.CompactedResults == 0 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestCompactHistoryRemovesOldTurnsAsALastResort(t *testing.T) {
	failed := func(id string) llm.ToolResult {
		return llm.ToolResult{CallID: id, IsError: true, Content: json.RawMessage(`{"content":[{"type":"text","text":"` + strings.Repeat("e", 6000) + `"}]}`)}
	}
	history := append(append(turnWith("first", failed("a"), failed("b")), turnWith("second", failed("c"))...), turnWith("third", failed("d"))...)
	compacted, stats := compactHistory(history, HistoryCompaction{})
	if stats.RemovedTurns != 1 || compacted[0].Text != "second" || len(compacted) != 8 {
		t.Fatalf("removed %d turns, first %q, %d messages", stats.RemovedTurns, compacted[0].Text, len(compacted))
	}
	// Errors are never changed.
	if !reflect.DeepEqual(compacted[2].ToolResults, history[6].ToolResults) {
		t.Fatal("an error result was changed")
	}
}

func TestPrepareHistoryDescribesTheHistorySent(t *testing.T) {
	service := &Service{config: withServiceDefaults(ServiceConfig{Backend: "om3"})}
	history := append(turnWith("first", llm.ToolResult{CallID: "a", Content: structuredResult(t, disks(24), true)}),
		turnWith("second", llm.ToolResult{CallID: "b", Content: structuredResult(t, disks(24), true)})...)
	sent, stats, err := service.prepareHistory(history)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Policy != "v1" || stats.MaxResultBytes != 2<<10 || stats.MaxHistoryResultBytes != 16<<10 || stats.CompactedResults != 2 ||
		stats.LoadedBytes != historySize(history) || stats.CompactedBytes >= stats.LoadedBytes || stats.SentBytes != historySize(sent) || stats.RemovedTurns != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	other := *stats
	other.SentBytes++
	run := TurnRun{History: stats}
	if run.Equal(TurnRun{History: &other}) || !run.Equal(TurnRun{History: stats}) || run.Equal(TurnRun{}) {
		t.Fatal("runs compare their history")
	}
}

func TestPrepareHistoryOfAFirstTurn(t *testing.T) {
	service := &Service{config: withServiceDefaults(ServiceConfig{Backend: "om3"})}
	for _, history := range [][]llm.Message{nil, {}} {
		_, stats, err := service.prepareHistory(history)
		if err != nil || stats.LoadedBytes != 0 || stats.CompactedBytes != 0 || stats.SentBytes != 0 {
			t.Fatalf("empty history stats = %+v, %v", stats, err)
		}
	}
}
