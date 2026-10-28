package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/opensvc/ai-agent/internal/llm"
)

const (
	// ReadResourceToolName is the tool of the agent the model reads the
	// resources of the MCP with: reference texts, such as the format of a
	// definition the tools write.
	ReadResourceToolName = "read_resource"
	// maxListedResources bounds the resources the description of
	// read_resource lists, and maxResourceSummaryBytes the description of
	// each: a server with many resources does not grow every request.
	maxListedResources      = 10
	maxResourceSummaryBytes = 240
	// maxResourceTextBytes bounds the text a read gives the model.
	maxResourceTextBytes = 64 << 10
)

// resourceShelf holds the resources a turn may read, as the MCP listed them,
// and the tool that reads them; none when the server has no text resource.
type resourceShelf struct {
	tool  *llm.Tool
	uris  []string
	names map[string]string
}

// newResourceShelf lists the resources of the session for read_resource, at
// most maxListedResources, but those of skillFiles: a skill is loaded by
// load_skill, which checks it, never read as a reference text. The name is
// the agent's: a tool of the MCP of the same name is refused, as
// search_tools is. A listing that fails leaves the turn without the tool, a
// call of it answered with a tool error.
func newResourceShelf(ctx context.Context, session MCPSession, catalog map[string]llm.Tool, skillFiles map[string]bool) (*resourceShelf, error) {
	shelf := &resourceShelf{names: map[string]string{}}
	if _, taken := catalog[ReadResourceToolName]; taken {
		return nil, fmt.Errorf("MCP tool %q conflicts with the resource reading of the agent", ReadResourceToolName)
	}
	resources, err := session.ListResources(ctx)
	if err != nil || len(resources) == 0 {
		return shelf, nil
	}
	var lines []string
	for _, resource := range resources {
		if resource == nil || resource.URI == "" || skillFiles[resource.URI] || len(shelf.uris) == maxListedResources {
			continue
		}
		if resource.MIMEType != "" && !strings.HasPrefix(resource.MIMEType, "text/") {
			continue
		}
		summary := resource.Description
		if summary == "" {
			summary = resource.Title
		}
		summary = cutText(summary, maxResourceSummaryBytes)
		shelf.uris = append(shelf.uris, resource.URI)
		shelf.names[resource.URI] = resource.Name
		lines = append(lines, fmt.Sprintf("%s: %s", resource.URI, summary))
	}
	if len(shelf.uris) == 0 {
		return shelf, nil
	}
	schema, _ := json.Marshal(map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"uri"},
		"properties": map[string]any{"uri": map[string]any{"type": "string", "enum": shelf.uris, "description": "the URI of the resource to read"}},
	})
	shelf.tool = &llm.Tool{
		Name: ReadResourceToolName,
		Description: "Read a reference text of the backend, such as the format of a definition a tool writes, before using the tools it documents. " +
			"Read it in the turn that needs it: a text read in an earlier turn is not kept. The resources: " + strings.Join(lines, "; "),
		InputSchema: schema,
	}
	return shelf, nil
}

// known tells whether a call reads a resource: any call of read_resource,
// the name being the agent's, though the turn lists no resource, as a turn
// resumed after a confirmation may when the listing fails then.
func (s *resourceShelf) known(name string) bool {
	return name == ReadResourceToolName
}

// read runs a read_resource call: the text contents of the resource, cut
// to maxResourceTextBytes. A refused read is a tool error for the model,
// never a failed turn.
func (s *resourceShelf) read(ctx context.Context, session MCPSession, call llm.ToolCall) llm.ToolResult {
	fail := func(message string) llm.ToolResult {
		return llm.ToolResult{CallID: call.ID, Content: textToolResult(message), IsError: true}
	}
	var arguments struct {
		URI string `json:"uri"`
	}
	if len(s.uris) == 0 {
		return fail("No resource of the backend can be read now: answer without it.")
	}
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil || !slices.Contains(s.uris, arguments.URI) {
		return fail("read_resource takes uri, one of the resources its description lists.")
	}
	result, err := session.ReadResource(ctx, arguments.URI)
	if err != nil {
		return fail(fmt.Sprintf("The resource %s could not be read: %v", arguments.URI, err))
	}
	var texts []string
	for _, contents := range result.Contents {
		if contents == nil || contents.Text == "" || (contents.MIMEType != "" && !strings.HasPrefix(contents.MIMEType, "text/")) {
			continue
		}
		texts = append(texts, contents.Text)
	}
	if len(texts) == 0 {
		return fail(fmt.Sprintf("The resource %s holds no text.", arguments.URI))
	}
	text := strings.Join(texts, "\n\n")
	if len(text) > maxResourceTextBytes {
		text = cutText(text, maxResourceTextBytes) + "\n\n[cut: the resource exceeds 64 KiB]"
	}
	return llm.ToolResult{CallID: call.ID, Content: textToolResult(text)}
}

// cutText cuts a text to max bytes, on a rune boundary.
func cutText(text string, max int) string {
	if len(text) <= max {
		return text
	}
	cut := text[:max]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
