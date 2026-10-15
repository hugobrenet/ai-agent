package om3

import (
	"strings"
	"testing"
)

func TestSystemPromptGroundsToolArguments(t *testing.T) {
	for _, instruction := range []string{
		"Never guess object paths, node names, resource identifiers, or other tool arguments.",
		"Only use identifiers provided by the user or returned by successful tool results.",
		"Examples in tool descriptions or schemas are illustrative and are never discovered identifiers.",
		"Never infer an identifier from an object name, naming convention, example, or failed tool output.",
		"If a prerequisite discovery tool fails or does not return a required identifier, do not call any dependent tool, even when a likely value can be inferred; stop that diagnostic branch and report the uncertainty.",
		"When multiple independent read-only tool calls are needed and all their arguments are already known, request them in the same step. Wait for prerequisite results before requesting dependent calls.",
		"Tool results of earlier turns may be partial: a _history_compaction field lists what was omitted. Treat omitted data as unknown. When you need omitted details, call an appropriate read-only tool, never again a tool that changes state. A new read is a new observation that may differ from the earlier one: when the user asks for an earlier value that was omitted, say it is no longer available.",
		"In tool provenance, observed_at is when the MCP collected a result, not when OpenSVC updated the underlying status; use daemon-provided updated_at when available to assess status age.",
	} {
		if !strings.Contains(SystemPrompt, instruction) {
			t.Errorf("system prompt is missing instruction %q", instruction)
		}
	}
}
