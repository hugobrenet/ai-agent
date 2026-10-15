package conversation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// withDefaults completes a policy: a field left at zero takes its default
// value.
func (c HistoryCompaction) withDefaults() HistoryCompaction {
	defaults := DefaultHistoryCompaction
	for _, field := range []struct{ value, fallback *int }{
		{&c.MaxResultBytes, &defaults.MaxResultBytes}, {&c.MaxHistoryResultBytes, &defaults.MaxHistoryResultBytes},
		{&c.ArrayItems, &defaults.ArrayItems}, {&c.MaxStringBytes, &defaults.MaxStringBytes},
		{&c.MaxOmissions, &defaults.MaxOmissions}, {&c.MaxDepth, &defaults.MaxDepth},
	} {
		if *field.value == 0 {
			*field.value = *field.fallback
		}
	}
	return c
}

// minResultBytes is the smallest budget of a result: a marker alone fits.
const minResultBytes = 512

// validate checks a completed policy.
func (c HistoryCompaction) validate() error {
	if c.MaxResultBytes < minResultBytes || c.MaxHistoryResultBytes < c.MaxResultBytes || c.ArrayItems < 1 ||
		c.MaxStringBytes < 1 || c.MaxOmissions < 1 || c.MaxDepth < 1 {
		return fmt.Errorf("%w: history compaction needs a result budget of at least %d bytes, a history budget not below it, and positive limits", ErrInvalid, minResultBytes)
	}
	return nil
}

// compactHistory returns a copy of history whose tool results fit the
// policy, what it did, and the indexes of the messages holding a compacted
// result, one per result. Small results and errors are kept as they are;
// the structured results are compacted, the oldest more strongly when all
// the results exceed MaxHistoryResultBytes. When they still exceed it, the
// oldest turn is left out whole and the turns kept are compacted again from
// their original results, so that a turn left out frees its room for the
// others. Calls, arguments and call IDs are kept.
func compactHistory(history []llm.Message, policy HistoryCompaction) ([]llm.Message, HistoryStats, []int) {
	policy = policy.withDefaults()
	stats := HistoryStats{Policy: HistoryCompactionPolicy, MaxResultBytes: policy.MaxResultBytes, MaxHistoryResultBytes: policy.MaxHistoryResultBytes}
	start := 0
	for {
		messages, total, compacted := policy.compactTurns(history[start:])
		if total <= policy.MaxHistoryResultBytes || start >= len(history) {
			stats.CompactedResults = len(compacted)
			return messages, stats, compacted
		}
		// The oldest turn is left out, and the others compacted again.
		next := start + 1
		for next < len(history) && history[next].Role != llm.RoleUser {
			next++
		}
		start = next
		stats.RemovedTurns++
	}
}

// compactTurns compacts the results of history, the oldest more strongly
// when they exceed the global budget, and returns the copy, the size of its
// results and the indexes of the messages holding a compacted result.
func (c HistoryCompaction) compactTurns(history []llm.Message) ([]llm.Message, int, []int) {
	messages := make([]llm.Message, len(history))
	type ref struct{ message, result int }
	var refs []ref
	for i, message := range history {
		message.ToolCalls = slices.Clone(message.ToolCalls)
		message.ToolResults = slices.Clone(message.ToolResults)
		for j := range message.ToolResults {
			refs = append(refs, ref{i, j})
		}
		messages[i] = message
	}
	compacted := map[ref]bool{}
	set := func(r ref, budget int) {
		original := history[r.message].ToolResults[r.result]
		result := &messages[r.message].ToolResults[r.result]
		if original.IsError || len(original.Content) <= budget {
			return
		}
		if content, ok := c.compactResult(original.Content, budget); ok && len(content) < len(result.Content) {
			result.Content = content
			compacted[r] = true
		}
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
	for _, r := range refs {
		set(r, c.MaxResultBytes)
	}
	// Over the global budget, the oldest results are reduced first, the
	// last turn last: to a quarter of their budget, then to a marker alone.
	for _, budget := range []int{c.MaxResultBytes / 4, 0} {
		for _, r := range refs {
			if total() <= c.MaxHistoryResultBytes {
				break
			}
			set(r, budget)
		}
	}
	indexes := make([]int, 0, len(compacted))
	for _, r := range refs {
		if compacted[r] {
			indexes = append(indexes, r.message)
		}
	}
	return messages, total(), indexes
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
	// The contents beside the structured content: the text copy the MCP SDK
	// adds is dropped, as the agent drops it since C0. Distinct texts, such
	// as a warning, keep the prefix of those small enough to be kept whole,
	// so that their indexes stay true, as the items of an array.
	contents, _ := envelope["content"].([]any)
	for _, content := range contents {
		if item, ok := content.(map[string]any); !ok || item["type"] != "text" {
			return nil, false
		}
	}
	var base []omission
	var kept []any
	if len(contents) > 0 && duplicatedText(contents, structured) {
		base = append(base, omission{Path: "/content", Kind: omittedContent, Omitted: len(contents)})
	} else if len(contents) > 0 {
		for i, content := range contents {
			if data, _ := json.Marshal(content); len(data) > c.MaxStringBytes {
				base = append(base, omission{Path: fmt.Sprintf("/content/%d", i), Kind: omittedValue, Omitted: len(data)})
				break
			}
			kept = append(kept, content)
		}
		if len(kept) < len(contents) {
			base = append(base, omission{Path: "/content", Kind: omittedArrayTail, Kept: len(kept), Omitted: len(contents) - len(kept)})
		}
	}
	encode := func(contents []any, value any, omissions []omission, withValue bool) json.RawMessage {
		marker := historyMarker{Policy: HistoryCompactionPolicy, Partial: true, OriginalBytes: len(raw), Omissions: omissions}
		if len(marker.Omissions) > c.MaxOmissions {
			marker.OmissionsOmitted = len(marker.Omissions) - c.MaxOmissions
			marker.Omissions = marker.Omissions[:c.MaxOmissions]
		}
		if contents == nil {
			contents = []any{}
		}
		out := map[string]any{"content": contents, "_history_compaction": marker}
		if withValue {
			out["structuredContent"] = value
		}
		data, _ := json.Marshal(out)
		return data
	}
	// The whole result fits the budget: contents, structured content and
	// marker. The texts kept are dropped before the structured content.
	attempts := []struct {
		contents []any
		base     []omission
	}{{kept, base}}
	if len(kept) > 0 {
		attempts = append(attempts, struct {
			contents []any
			base     []omission
		}{nil, []omission{{Path: "/content", Kind: omittedArrayTail, Omitted: len(contents)}}})
	}
	for _, attempt := range attempts {
		for _, items := range c.itemSteps() {
			omissions := slices.Clone(attempt.base)
			value := c.compactValue(structured, "/structuredContent", items, 0, &omissions)
			if data := encode(attempt.contents, value, omissions, true); len(data) <= budget {
				return data, true
			}
		}
	}
	// The last resort is a marker alone, of two omissions: it fits the
	// smallest budget of a result.
	whole, _ := json.Marshal(structured)
	final := []omission{{Path: "/structuredContent", Kind: omittedValue, Omitted: len(whole)}}
	if len(contents) > 0 {
		final = append([]omission{{Path: "/content", Kind: omittedArrayTail, Omitted: len(contents)}}, final...)
	}
	return encode(nil, nil, final, false), true
}

// itemSteps are the numbers of items kept per collection, tried in turn.
func (c HistoryCompaction) itemSteps() []int {
	steps := []int{}
	for items := c.ArrayItems; items > 0; items /= 2 {
		steps = append(steps, items)
	}
	return append(steps, 0)
}

// duplicatedText tells contents made of one text holding the structured
// content, without annotations or metadata, as the MCP SDK adds it: the
// conditions under which the agent sends a structured result once.
func duplicatedText(contents []any, structured any) bool {
	if len(contents) != 1 {
		return false
	}
	item, ok := contents[0].(map[string]any)
	if !ok || item["type"] != "text" || len(item) != 2 {
		return false
	}
	text, ok := item["text"].(string)
	if !ok {
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return false
	}
	// Only the end of the text may follow the value: a text with anything
	// after it, such as a warning, is distinct.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return false
	}
	fromText, errText := json.Marshal(value)
	fromStructured, errStructured := json.Marshal(structured)
	return errText == nil && errStructured == nil && bytes.Equal(fromText, fromStructured)
}

// largeObjectEntries is the size from which an object is a dictionary,
// whatever its values: only its first entries are kept.
func (c HistoryCompaction) largeObjectEntries() int {
	return 4 * c.ArrayItems
}

// compactValue keeps the scalars and the small objects of scalars, such as
// a provenance, and the first items of the collections, recording every
// omission. Numbers are kept as written; kept strings are never changed: a
// longer one is omitted whole. A collection nested deeper than MaxDepth is
// omitted. An array keeps a prefix of its items, so that their indexes stay
// true: it stops before an item it cannot keep whole.
func (c HistoryCompaction) compactValue(value any, path string, items int, depth int, omissions *[]omission) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		// A large object is a dictionary, such as a status per node: only
		// its first entries are kept, whatever their values.
		if len(keys) > c.largeObjectEntries() {
			*omissions = append(*omissions, omission{Path: path, Kind: omittedObjectEntries, Kept: items, Omitted: len(keys) - items})
			keys = keys[:items]
		}
		out := make(map[string]any, len(keys))
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
			case collections >= max(items, 1) && len(keys) > c.ArrayItems || items == 0:
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
		out := make([]any, 0, min(items, len(typed)))
		for i, child := range typed {
			if i == items {
				break
			}
			childPath := fmt.Sprintf("%s/%d", path, i)
			if text, ok := child.(string); ok && len(text) > c.MaxStringBytes {
				*omissions = append(*omissions, omission{Path: childPath, Kind: omittedLongString, Omitted: len(text)})
				break
			}
			if isCollection(child) && depth+1 > c.MaxDepth && !c.isSmallScalarObject(child) {
				data, _ := json.Marshal(child)
				*omissions = append(*omissions, omission{Path: childPath, Kind: omittedDepth, Omitted: len(data)})
				break
			}
			out = append(out, c.compactValue(child, childPath, items, depth+1, omissions))
		}
		if len(typed) > len(out) {
			*omissions = append(*omissions, omission{Path: path, Kind: omittedArrayTail, Kept: len(out), Omitted: len(typed) - len(out)})
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
