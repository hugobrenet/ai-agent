package agent

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/ai-agent/internal/llm"
	"github.com/opensvc/ai-agent/internal/toolsearch"
)

const (
	// SearchToolName is the tool of the agent the model searches the MCP
	// catalog with, when the catalog is too large to be shown whole.
	SearchToolName = "search_tools"
	// DefaultSearchThreshold is the largest catalog shown whole: beyond, the
	// model sees the core tools and search_tools.
	DefaultSearchThreshold = 20
	// CoreToolMeta marks, in the _meta of an MCP tool, a tool always shown.
	CoreToolMeta = "opensvc/core"

	maxSearchesPerTurn    = 3
	defaultSearchResults  = 5
	maxSearchResults      = 10
	maxHistoryTools       = 8
	maxSearchSummaryBytes = 240
	// maxModelTools bounds the tools of a suspended turn state.
	maxModelTools = 1024
)

// searchInstruction is added to the system prompt when the model sees a
// part of the catalog only.
const searchInstruction = "Only some tools are listed. When none fits, call " + SearchToolName +
	" with English keywords describing the data or the action you need; the tools it finds can be called from the next step on."

var searchToolSchema = json.RawMessage(`{"type":"object","properties":{` +
	`"query":{"type":"string","description":"English keywords of the data or action needed, such as node hardware inventory"},` +
	`"limit":{"type":"integer","minimum":1,"maximum":10,"description":"optional number of tools to return; defaults to 5"}},` +
	`"required":["query"],"additionalProperties":false}`)

var searchTool = llm.Tool{
	Name: SearchToolName,
	Description: "Search the tools of the backend by keywords, when none of the listed tools fits the question. " +
		"Returns the best matching tools with a summary; they can be called from the next step on.",
	InputSchema: searchToolSchema,
}

// toolSelection holds the tools the model sees during a turn: the whole
// catalog when it is small, else search_tools and an active set made of the
// core tools, the tools of the recent history and the tools found.
type toolSelection struct {
	search   bool
	catalog  map[string]llm.Tool
	all      []llm.Tool
	index    *toolsearch.Index
	active   []string
	searches int
}

// indexCache keeps the index of the last catalog, by its hash.
type indexCache struct {
	mu    sync.Mutex
	hash  string
	index *toolsearch.Index
}

func (c *indexCache) get(hash string, tools []*mcp.Tool) (*toolsearch.Index, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.index != nil && c.hash == hash {
		return c.index, nil
	}
	index, err := toolsearch.New(toolsearch.FromMCP(tools))
	if err != nil {
		return nil, fmt.Errorf("index MCP tools: %w", err)
	}
	c.hash, c.index = hash, index
	return index, nil
}

func (a *Agent) selectTools(mcpTools []*mcp.Tool, converted []llm.Tool, catalogHash string, history []llm.Message, state *SuspendedTurn) (*toolSelection, error) {
	selection := &toolSelection{all: converted, catalog: make(map[string]llm.Tool, len(converted))}
	for _, tool := range converted {
		selection.catalog[tool.Name] = tool
	}
	threshold := a.config.SearchThreshold
	if threshold == 0 {
		threshold = DefaultSearchThreshold
	}
	if threshold < 0 || len(converted) <= threshold {
		return selection, nil
	}
	if _, exists := selection.catalog[SearchToolName]; exists {
		return nil, fmt.Errorf("MCP tool %q conflicts with the tool search of the agent", SearchToolName)
	}
	index, err := a.indexes.get(catalogHash, mcpTools)
	if err != nil {
		return nil, err
	}
	selection.search, selection.index, selection.searches = true, index, state.Searches
	for _, tool := range mcpTools {
		if core, _ := tool.Meta[CoreToolMeta].(bool); core {
			selection.add(tool.Name)
		}
	}
	if len(selection.active) == 0 {
		for _, name := range a.config.CoreTools {
			selection.add(name)
		}
	}
	// The tools of the last turns, the most recent first, for the questions
	// that follow them.
	recent := 0
	for i := len(history) - 1; i >= 0 && recent < maxHistoryTools; i-- {
		for _, call := range slices.Backward(history[i].ToolCalls) {
			if recent < maxHistoryTools && selection.add(call.Name) {
				recent++
			}
		}
	}
	for _, name := range state.ActiveTools {
		selection.add(name)
	}
	return selection, nil
}

// add activates a catalog tool, and reports whether it was not active yet.
func (s *toolSelection) add(name string) bool {
	if _, ok := s.catalog[name]; !ok || slices.Contains(s.active, name) {
		return false
	}
	s.active = append(s.active, name)
	return true
}

// tools are the tools of the next model request. Found tools come last, so
// that the start of the list stays the same within the turn.
func (s *toolSelection) tools() []llm.Tool {
	if !s.search {
		return s.all
	}
	tools := make([]llm.Tool, 0, len(s.active)+1)
	tools = append(tools, searchTool)
	for _, name := range s.active {
		tools = append(tools, s.catalog[name])
	}
	return tools
}

// known tells whether the model may call a tool: any tool of the catalog,
// shown or not, and search_tools when the search is on.
func (s *toolSelection) known(name string) bool {
	_, ok := s.catalog[name]
	return ok || (s.search && name == SearchToolName)
}

type searchArguments struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type searchMatch struct {
	Name    string  `json:"name"`
	Title   string  `json:"title,omitempty"`
	Summary string  `json:"summary"`
	Score   float64 `json:"score"`
}

// run executes a search_tools call: the tools found become active. A
// refused search is a tool error for the model, never a failed turn.
func (s *toolSelection) run(call llm.ToolCall, titles map[string]string) llm.ToolResult {
	fail := func(message string) llm.ToolResult {
		return llm.ToolResult{CallID: call.ID, Content: textToolResult(message), IsError: true}
	}
	if s.searches >= maxSearchesPerTurn {
		return fail(fmt.Sprintf("The tool search is limited to %d searches per question. Answer with the tools available.", maxSearchesPerTurn))
	}
	var arguments searchArguments
	decoder := json.NewDecoder(strings.NewReader(string(call.Arguments)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return fail("search_tools takes a query, English keywords, and an optional limit.")
	}
	if arguments.Limit == 0 {
		arguments.Limit = defaultSearchResults
	}
	if arguments.Limit < 1 || arguments.Limit > maxSearchResults {
		return fail(fmt.Sprintf("The limit of search_tools must be between 1 and %d.", maxSearchResults))
	}
	s.searches++
	results, err := s.index.Search(arguments.Query, arguments.Limit)
	if err != nil {
		return fail("The query of search_tools must be a few English keywords.")
	}
	matches := make([]searchMatch, 0, len(results))
	for _, result := range results {
		s.add(result.Name)
		matches = append(matches, searchMatch{
			Name: result.Name, Title: titles[result.Name], Summary: summary(s.catalog[result.Name].Description),
			Score: math.Round(result.Score*100) / 100,
		})
	}
	text, _ := json.Marshal(map[string]any{"tools": matches})
	if len(matches) == 0 {
		text = []byte(`{"tools":[],"hint":"No tool matches these keywords. Try other English words, or answer with the tools available."}`)
	}
	return llm.ToolResult{CallID: call.ID, Content: textToolResult(string(text))}
}

// summary is the first sentence of a description, bounded.
func summary(description string) string {
	if end := strings.Index(description, ". "); end >= 0 {
		description = description[:end+1]
	}
	if len(description) > maxSearchSummaryBytes {
		cut := maxSearchSummaryBytes
		for cut > 0 && !utf8.RuneStart(description[cut]) {
			cut--
		}
		description = description[:cut] + "…"
	}
	return description
}

// textToolResult is a tool result holding one text, as MCP returns it.
func textToolResult(text string) json.RawMessage {
	data, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": text}}})
	return data
}
