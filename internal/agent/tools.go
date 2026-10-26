package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// maxToolSummaryBytes bounds the summary of a tool, the first sentence
	// of its description, and maxToolTitleBytes its title; past them, each
	// is cut on a character and ends with an ellipsis.
	maxToolSummaryBytes = 512
	maxToolTitleBytes   = 200
	// toolsTimeout bounds the reading of the tools of the MCP.
	toolsTimeout = 30 * time.Second
)

// ToolInfo is a tool of the MCP the model may be offered, as a user reads
// it: its title, the first sentence of its description, whether it only
// reads, and whether a call of it waits for the confirmation of the user.
type ToolInfo struct {
	Name        string
	Title       string
	Summary     string
	ReadOnly    bool
	Confirmable bool
}

// Tools lists the tools of the MCP the model may be offered, the catalog of
// the deployment, read with the token of the user of ctx; the tools for the
// client only, such as the previews, are left out. The MCP serves the same
// catalog to every user: what a call returns depends on the rights of the
// user, checked at the call.
func (a *Agent) Tools(ctx context.Context) (tools []ToolInfo, err error) {
	ctx, cancel := context.WithTimeout(ctx, min(a.config.Timeout, toolsTimeout))
	defer cancel()
	session, err := a.connectMCP(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect agent to MCP: %w", err)
	}
	defer func() {
		if closeErr := session.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close agent MCP session: %w", closeErr))
		}
	}()
	mcpTools, err := session.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list agent MCP tools: %w", err)
	}
	tools = []ToolInfo{}
	for _, tool := range modelTools(mcpTools) {
		tools = append(tools, ToolInfo{
			Name:        tool.Name,
			Title:       bounded(toolTitle(tool), maxToolTitleBytes),
			Summary:     bounded(firstSentence(tool.Description), maxToolSummaryBytes),
			ReadOnly:    tool.Annotations != nil && tool.Annotations.ReadOnlyHint,
			Confirmable: requiresConfirmation(tool),
		})
	}
	return tools, nil
}

// firstSentence is the first sentence of a description: up to its first
// period followed by a space or a line break, else the whole description.
func firstSentence(description string) string {
	description = strings.TrimSpace(description)
	for index := 0; index+1 < len(description); index++ {
		if description[index] == '.' && (description[index+1] == ' ' || description[index+1] == '\n') {
			return description[:index+1]
		}
	}
	return description
}

// bounded cuts text on a character past limit bytes, with an ellipsis.
func bounded(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}
