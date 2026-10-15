package agent

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
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

// searchKeywordsGuidance tells the model how to write a search query: a
// whole question brings words that favor the wrong tools.
const searchKeywordsGuidance = "Use 3–8 English keywords describing the required capability. Omit greetings, full sentences, and specific resource identifiers."

// searchInstruction is added to the system prompt when the model sees a
// part of the catalog only.
const searchInstruction = "Only some tools are listed. When none fits, call " + SearchToolName +
	"; the tools it finds can be called from the next step on. " + searchKeywordsGuidance + " " + searchBatchGuidance

// searchBatchGuidance asks the model to request the tools a search found
// together, as the backend prompts do for independent calls.
const searchBatchGuidance = "After a search, request the independent read-only tools you need together in the next step, when all their arguments are already known. Wait for prerequisite results before making dependent calls."

// searchToolSchema describes the bounds of the query. toolsearch counts
// toolsearch.MaxQueryBytes in UTF-8 bytes, while maxLength counts characters:
// maxLength is only the loosest bound a schema can state, and the
// description gives the exact one. A query without a searchable word is
// refused, and only its first toolsearch.MaxQueryTerms distinct terms are
// searched.
var searchToolSchema = json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{`+
	`"query":{"type":"string","minLength":1,"maxLength":%d,"description":%q},`+
	`"limit":{"type":"integer","minimum":1,"maximum":%d,"description":"optional number of tools to return; defaults to %d"}},`+
	`"required":["query"],"additionalProperties":false}`,
	toolsearch.MaxQueryBytes, searchKeywordsGuidance+" At most "+strconv.Itoa(toolsearch.MaxQueryBytes)+" UTF-8 bytes. Example: node hardware inventory.",
	maxSearchResults, defaultSearchResults))

var searchTool = llm.Tool{
	Name: SearchToolName,
	Description: "Search the tools of the backend when none of the listed tools fits the question. " + searchKeywordsGuidance +
		" Returns the best matching tools with a summary; they can be called from the next step on.",
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
	// bytes is the size of the tools shown, bounded by
	// maxModelToolCatalogBytes.
	bytes int
	// shown are the tools sent to the model during the turn, in order.
	shown []string
	// core are the tools never removed to make room for the tools found.
	core map[string]bool
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
	catalogBytes := 0
	for _, tool := range converted {
		selection.catalog[tool.Name] = tool
		catalogBytes += toolBytes(tool)
	}
	threshold := a.config.SearchThreshold
	if threshold == 0 {
		threshold = DefaultSearchThreshold
	}
	// A turn suspended while searching keeps searching when it resumes, so
	// that the search calls it planned still run in the agent. A catalog
	// too large for one model request is searched whatever the threshold.
	search := state.ToolSearch || catalogBytes > maxModelToolCatalogBytes || (threshold > 0 && len(converted) > threshold)
	if !search {
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
	selection.core = map[string]bool{}
	selection.bytes = toolBytes(searchTool)
	state.ToolSearch = true
	for _, tool := range mcpTools {
		if core, _ := tool.Meta[CoreToolMeta].(bool); core && selection.add(tool.Name) {
			selection.core[tool.Name] = true
		}
	}
	if len(selection.active) == 0 {
		for _, name := range a.config.CoreTools {
			if selection.add(name) {
				selection.core[name] = true
			}
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

// add activates a catalog tool, and reports whether it was not active yet. A
// tool beyond the size of a model request is not shown.
func (s *toolSelection) add(name string) bool {
	tool, ok := s.catalog[name]
	if !ok || slices.Contains(s.active, name) || s.bytes+toolBytes(tool) > maxModelToolCatalogBytes {
		return false
	}
	s.active = append(s.active, name)
	s.bytes += toolBytes(tool)
	return true
}

// show activates a tool a search found. When the tools shown leave no room,
// the oldest tools that are not core tools are removed, the tools of the
// history first, to make room for it. It reports whether the tool is shown.
func (s *toolSelection) show(name string) bool {
	if slices.Contains(s.active, name) {
		return true
	}
	tool, ok := s.catalog[name]
	if !ok {
		return false
	}
	for s.bytes+toolBytes(tool) > maxModelToolCatalogBytes {
		oldest := slices.IndexFunc(s.active, func(active string) bool { return !s.core[active] })
		if oldest < 0 {
			return false
		}
		s.bytes -= toolBytes(s.catalog[s.active[oldest]])
		s.active = slices.Delete(s.active, oldest, oldest+1)
	}
	return s.add(name)
}

// tools are the tools of the next model request. Found tools come last, so
// that the start of the list stays the same within the turn.
func (s *toolSelection) tools() []llm.Tool {
	tools := s.all
	if s.search {
		tools = make([]llm.Tool, 0, len(s.active)+1)
		tools = append(tools, searchTool)
		for _, name := range s.active {
			tools = append(tools, s.catalog[name])
		}
	}
	for _, tool := range tools {
		if !slices.Contains(s.shown, tool.Name) {
			s.shown = append(s.shown, tool.Name)
		}
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
	// Shown is false for a tool found but not shown: its definition does
	// not fit in a model request beside the core tools.
	Shown bool `json:"shown"`
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
		return fail("search_tools takes a query and an optional limit. " + searchKeywordsGuidance)
	}
	if arguments.Limit == 0 {
		arguments.Limit = defaultSearchResults
	}
	if arguments.Limit < 1 || arguments.Limit > maxSearchResults {
		return fail(fmt.Sprintf("The limit of search_tools must be between 1 and %d.", maxSearchResults))
	}
	results, err := s.index.Search(arguments.Query, arguments.Limit)
	if err != nil {
		return fail("The query of search_tools has no searchable keyword, or exceeds " + strconv.Itoa(toolsearch.MaxQueryBytes) + " bytes. " + searchKeywordsGuidance)
	}
	// Only a valid search counts, whether it finds tools or not.
	s.searches++
	matches := make([]searchMatch, 0, len(results))
	hidden := false
	for _, result := range results {
		shown := s.show(result.Name)
		hidden = hidden || !shown
		matches = append(matches, searchMatch{
			Name: result.Name, Title: titles[result.Name], Summary: summary(s.catalog[result.Name].Description),
			Score: math.Round(result.Score*100) / 100, Shown: shown,
		})
	}
	response := map[string]any{"tools": matches}
	switch {
	case len(matches) == 0:
		response["hint"] = "No tool matches these keywords. Try other English words, or answer with the tools available."
	case hidden:
		response["hint"] = "The tools with shown false were found but cannot be shown: their definitions exceed the size of a model request. Answer with the tools shown."
	}
	text, _ := json.Marshal(response)
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
