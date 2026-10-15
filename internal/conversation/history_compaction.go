package conversation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/opensvc/ai-agent/internal/llm"
)

// HistoryCompactionPolicy names the compaction of the history below; it is
// recorded with each run, so that a change of policy is visible in the
// feedback.
const HistoryCompactionPolicy = "v1"

// HistoryCompaction bounds the tool results of the earlier turns a model
// request carries. The stored messages are never changed: the model
// receives a compact copy, whose omissions are listed in a
// _history_compaction field of each result.
type HistoryCompaction struct {
	// MaxResultBytes bounds one compacted result, its marker included.
	MaxResultBytes int
	// MaxHistoryResultBytes bounds all the tool results of the history.
	MaxHistoryResultBytes int
	// ArrayItems is the number of items first kept of a collection, then
	// reduced until a result fits.
	ArrayItems int
	// MaxStringBytes is the longest string kept; a longer one is omitted,
	// never cut: a generic compaction cannot tell a text from an identifier.
	MaxStringBytes int
	// MaxOmissions bounds the omissions listed in a marker.
	MaxOmissions int
	// MaxDepth bounds the nesting kept.
	MaxDepth int
}

// DefaultHistoryCompaction is the experimental first policy.
var DefaultHistoryCompaction = HistoryCompaction{
	MaxResultBytes:        2 << 10,
	MaxHistoryResultBytes: 16 << 10,
	ArrayItems:            8,
	MaxStringBytes:        256,
	MaxOmissions:          16,
	MaxDepth:              6,
}

// HistoryStats describes the history a turn sent to the model.
type HistoryStats struct {
	Policy                string `json:"policy"`
	MaxResultBytes        int    `json:"max_result_bytes"`
	MaxHistoryResultBytes int    `json:"max_history_result_bytes"`
	// LoadedBytes, CompactedBytes and SentBytes are the sizes of the
	// history loaded, compacted, and sent once bounded.
	LoadedBytes    int `json:"loaded_bytes"`
	CompactedBytes int `json:"compacted_bytes"`
	SentBytes      int `json:"sent_bytes"`
	// CompactedResults counts the tool results compacted, RemovedTurns the
	// earlier turns left out.
	CompactedResults int `json:"compacted_results"`
	RemovedTurns     int `json:"removed_turns"`
}

// historyMarker is the _history_compaction field of a compacted result.
type historyMarker struct {
	Policy           string     `json:"policy"`
	Partial          bool       `json:"partial"`
	OriginalBytes    int        `json:"original_bytes"`
	Omissions        []omission `json:"omissions"`
	OmissionsOmitted int        `json:"omissions_omitted,omitempty"`
}

// omission is a part of a result left out: at path, a JSON pointer into the
// result, the kind of omission, the items kept and the items omitted, or
// the bytes omitted for a value.
type omission struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Kept    int    `json:"kept"`
	Omitted int    `json:"omitted"`
}

// Kinds of omissions.
const (
	omittedArrayTail     = "array_tail"     // the items after the first ones
	omittedObjectEntries = "object_entries" // the entries holding collections, after the first ones
	omittedLongString    = "long_string"    // a string longer than MaxStringBytes, in bytes
	omittedDepth         = "depth"          // a value nested deeper than MaxDepth, in bytes
	omittedContent       = "content"        // the text contents beside the structured content
	omittedValue         = "value"          // the whole structured content, in bytes
)

func (c HistoryCompaction) withDefaults() HistoryCompaction {
	if c == (HistoryCompaction{}) {
		return DefaultHistoryCompaction
	}
	return c
}

// compactHistory returns a copy of history whose tool results fit the
// policy, and what it did. Small results and errors are kept as they are;
// the structured results are compacted, the oldest more strongly when all
// the results exceed MaxHistoryResultBytes, and as a last resort the oldest
// turns are left out whole. Calls, arguments and call IDs are kept.
func compactHistory(history []llm.Message, policy HistoryCompaction) ([]llm.Message, HistoryStats) {
	policy = policy.withDefaults()
	stats := HistoryStats{Policy: HistoryCompactionPolicy, MaxResultBytes: policy.MaxResultBytes, MaxHistoryResultBytes: policy.MaxHistoryResultBytes}
	messages := make([]llm.Message, len(history))
	type ref struct{ message, result, turn int }
	var refs []ref
	turn := -1
	for i, message := range history {
		if message.Role == llm.RoleUser {
			turn++
		}
		message.ToolCalls = slices.Clone(message.ToolCalls)
		message.ToolResults = slices.Clone(message.ToolResults)
		for j := range message.ToolResults {
			refs = append(refs, ref{i, j, turn})
		}
		messages[i] = message
	}
	original := func(r ref) json.RawMessage { return history[r.message].ToolResults[r.result].Content }
	compacted := map[ref]bool{}
	set := func(r ref, budget int) {
		result := &messages[r.message].ToolResults[r.result]
		if result.IsError || len(original(r)) <= budget {
			return
		}
		if content, ok := policy.compactResult(original(r), budget); ok && len(content) < len(result.Content) {
			result.Content = content
			compacted[r] = true
		}
	}
	for _, r := range refs {
		set(r, policy.MaxResultBytes)
	}
	total := func() int {
		size := 0
		for _, message := range messages {
			for _, result := range message.ToolResults {
				size += len(result.Content)
			}
		}
		return size
	}
	// Over the global budget, the oldest results are reduced first, the
	// last turn last: to a quarter of their budget, then to a marker alone.
	for _, budget := range []int{policy.MaxResultBytes / 4, 0} {
		for _, r := range refs {
			if total() <= policy.MaxHistoryResultBytes {
				break
			}
			set(r, budget)
		}
	}
	// As a last resort, the oldest turns are left out whole, so that the
	// messages stay a valid sequence of turns.
	start := 0
	for total() > policy.MaxHistoryResultBytes && start < len(messages) {
		next := start + 1
		for next < len(messages) && messages[next].Role != llm.RoleUser {
			next++
		}
		for i := start; i < next; i++ {
			messages[i].ToolResults = nil
		}
		start = next
		stats.RemovedTurns++
	}
	messages = messages[start:]
	for r := range compacted {
		if r.message >= start {
			stats.CompactedResults++
		}
	}
	return messages, stats
}

// compactResult compacts a structured result to at most budget bytes, its
// marker included, reducing its collections step by step, or to a marker
// alone. It reports false for a result it does not handle: one without
// structured content, or with contents other than text.
func (c HistoryCompaction) compactResult(raw json.RawMessage, budget int) (json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var envelope map[string]any
	if err := decoder.Decode(&envelope); err != nil {
		return nil, false
	}
	structured, ok := envelope["structuredContent"]
	if !ok || structured == nil {
		return nil, false
	}
	var base []omission
	if contents, ok := envelope["content"].([]any); ok && len(contents) > 0 {
		for _, content := range contents {
			if item, ok := content.(map[string]any); !ok || item["type"] != "text" {
				return nil, false
			}
		}
		base = append(base, omission{Path: "/content", Kind: omittedContent, Omitted: len(contents)})
	}
	encode := func(value any, omissions []omission, withValue bool) json.RawMessage {
		marker := historyMarker{Policy: HistoryCompactionPolicy, Partial: true, OriginalBytes: len(raw), Omissions: omissions}
		if len(marker.Omissions) > c.MaxOmissions {
			marker.OmissionsOmitted = len(marker.Omissions) - c.MaxOmissions
			marker.Omissions = marker.Omissions[:c.MaxOmissions]
		}
		out := map[string]any{"content": []any{}, "_history_compaction": marker}
		if withValue {
			out["structuredContent"] = value
		}
		data, _ := json.Marshal(out)
		return data
	}
	for _, items := range c.itemSteps() {
		omissions := slices.Clone(base)
		value := c.compactValue(structured, "/structuredContent", items, 0, &omissions)
		if data := encode(value, omissions, true); len(data) <= budget {
			return data, true
		}
	}
	whole, _ := json.Marshal(structured)
	return encode(nil, append(base, omission{Path: "/structuredContent", Kind: omittedValue, Omitted: len(whole)}), false), true
}

// itemSteps are the numbers of items kept per collection, tried in turn.
func (c HistoryCompaction) itemSteps() []int {
	steps := []int{}
	for items := c.ArrayItems; items > 0; items /= 2 {
		steps = append(steps, items)
	}
	return append(steps, 0)
}

// compactValue keeps the scalars and the small objects of scalars, such as
// a provenance, and the first items of the collections, recording every
// omission. Numbers are kept as written; kept strings are never changed.
func (c HistoryCompaction) compactValue(value any, path string, items int, depth int, omissions *[]omission) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(typed))
		collections := 0
		var omitted []string
		for _, key := range keys {
			child, childPath := typed[key], path+"/"+pointerToken(key)
			switch {
			case isScalar(child):
				if text, ok := child.(string); ok && len(text) > c.MaxStringBytes {
					*omissions = append(*omissions, omission{Path: childPath, Kind: omittedLongString, Omitted: len(text)})
					continue
				}
				out[key] = child
			case c.isSmallScalarObject(child):
				out[key] = child
			case depth+1 > c.MaxDepth:
				data, _ := json.Marshal(child)
				*omissions = append(*omissions, omission{Path: childPath, Kind: omittedDepth, Omitted: len(data)})
			case collections >= max(items, 1) && len(keys) > c.ArrayItems || items == 0 && isCollection(child):
				omitted = append(omitted, key)
			default:
				collections++
				out[key] = c.compactValue(child, childPath, items, depth+1, omissions)
			}
		}
		if len(omitted) > 0 {
			*omissions = append(*omissions, omission{Path: path, Kind: omittedObjectEntries, Kept: collections, Omitted: len(omitted)})
		}
		return out
	case []any:
		kept := min(items, len(typed))
		out := make([]any, 0, kept)
		for i := range kept {
			out = append(out, c.compactValue(typed[i], fmt.Sprintf("%s/%d", path, i), items, depth+1, omissions))
		}
		if len(typed) > kept {
			*omissions = append(*omissions, omission{Path: path, Kind: omittedArrayTail, Kept: kept, Omitted: len(typed) - kept})
		}
		return out
	default:
		return value
	}
}

func isScalar(value any) bool {
	switch value.(type) {
	case nil, bool, string, json.Number, float64:
		return true
	}
	return false
}

func isCollection(value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

// isSmallScalarObject tells an object of a few short scalars, such as a
// provenance, kept whole.
func (c HistoryCompaction) isSmallScalarObject(value any) bool {
	object, ok := value.(map[string]any)
	if !ok || len(object) > c.ArrayItems {
		return false
	}
	for _, child := range object {
		if !isScalar(child) {
			return false
		}
		if text, ok := child.(string); ok && len(text) > c.MaxStringBytes {
			return false
		}
	}
	return true
}

// pointerToken escapes a key as a JSON pointer token.
func pointerToken(key string) string {
	return strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

// historySize is the encoded size of messages, 0 for none.
func historySize(messages []llm.Message) int {
	if len(messages) == 0 {
		return 0
	}
	data, _ := json.Marshal(messages)
	return len(data)
}
