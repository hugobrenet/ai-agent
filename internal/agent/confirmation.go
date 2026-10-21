package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/ai-agent/internal/llm"
)

// Decision is the answer of the user to a confirmation request.
type Decision string

const (
	DecisionApprove Decision = "approve"
	DecisionReject  Decision = "reject"
)

func (d Decision) Valid() bool {
	return d == DecisionApprove || d == DecisionReject
}

// rejectedToolResult is the tool result the model receives for an action the
// user rejected: the call was not executed.
var rejectedToolResult = json.RawMessage(`{"content":[{"type":"text","text":"The user rejected this action. It was not executed."}]}`)

// SuspendedTurn is the state of a turn waiting for the user to confirm a tool
// call. It holds everything RunTurn needs to resume: the messages of the turn
// so far, and the calls of the current model iteration with the results
// already obtained. The pending call is Calls[len(Results)].
type SuspendedTurn struct {
	Messages       []llm.Message    `json:"messages"`
	Iteration      int              `json:"iteration"`
	TotalToolCalls int              `json:"total_tool_calls"`
	Text           string           `json:"text"`
	Calls          []llm.ToolCall   `json:"calls"`
	Results        []llm.ToolResult `json:"results"`
	// ActiveTools are the tools the searches of the turn found, and
	// Searches their count, for the resumed turn to see the same tools.
	ActiveTools []string `json:"active_tools,omitempty"`
	Searches    int      `json:"searches,omitempty"`
	// ToolSearch tells that the turn searched the catalog: it keeps
	// searching when it resumes, so that its planned search calls run.
	ToolSearch bool   `json:"tool_search,omitempty"`
	ToolTitle  string `json:"tool_title"`
	// ArgumentTypes are the media types the input schema of the pending
	// tool gives its text arguments, for the client to show them.
	ArgumentTypes map[string]string `json:"argument_types,omitempty"`
}

// PendingCall returns the call waiting for confirmation.
func (s SuspendedTurn) PendingCall() (llm.ToolCall, error) {
	if len(s.Results) >= len(s.Calls) {
		return llm.ToolCall{}, fmt.Errorf("suspended turn has no pending tool call")
	}
	return s.Calls[len(s.Results)], nil
}

func (s SuspendedTurn) validate(maxIterations int) error {
	if len(s.Messages) == 0 || s.Messages[0].Role != llm.RoleUser {
		return fmt.Errorf("suspended turn does not start with a user message")
	}
	if s.Iteration <= 0 || s.Iteration >= maxIterations {
		return fmt.Errorf("suspended turn iteration %d is out of range", s.Iteration)
	}
	if len(s.Calls) == 0 || len(s.Calls) > maxToolCallsPerTurn || len(s.Results) >= len(s.Calls) {
		return fmt.Errorf("suspended turn has no pending tool call")
	}
	if s.Searches < 0 || s.Searches > maxSearchesPerTurn || len(s.ActiveTools) > maxModelTools {
		return fmt.Errorf("suspended turn tool search state is out of range")
	}
	if len(s.ArgumentTypes) > maxArgumentTypes {
		return fmt.Errorf("suspended turn has more than %d argument types", maxArgumentTypes)
	}
	if s.TotalToolCalls < len(s.Calls) || s.TotalToolCalls > maxToolCallsPerAsk {
		return fmt.Errorf("suspended turn tool call count %d is out of range", s.TotalToolCalls)
	}
	for index, result := range s.Results {
		if result.CallID != s.Calls[index].ID {
			return fmt.Errorf("suspended turn result %d does not match its call", index)
		}
	}
	return nil
}

// ArgumentsHash binds a confirmation to the exact arguments shown to the user.
func ArgumentsHash(arguments json.RawMessage) string {
	sum := sha256.Sum256(arguments)
	return hex.EncodeToString(sum[:])
}

// Confirmation describes a tool call waiting for the decision of the user.
type Confirmation struct {
	ID          string
	TurnID      string
	ToolName    string
	ToolTitle   string
	Destructive bool
	Arguments   json.RawMessage
	// ArgumentTypes are the media types of the text arguments, such as
	// application/yaml, as the input schema of the tool declares them.
	ArgumentTypes map[string]string
	ExpiresAt     time.Time
}

// requiresConfirmation tells whether a tool call must be confirmed by the
// user before it runs. MCP annotations are hints: a missing destructiveHint
// on a tool that is not read-only means destructive, so the absence of
// annotations requires a confirmation.
func requiresConfirmation(tool *mcp.Tool) bool {
	annotations := tool.Annotations
	if annotations != nil && annotations.ReadOnlyHint {
		return false
	}
	if annotations == nil || annotations.DestructiveHint == nil {
		return true
	}
	return *annotations.DestructiveHint
}

// toolTitle is the human title of a tool, its name when it declares none.
// maxArgumentTypes bounds the argument types a confirmation gives, and
// maxArgumentType the length of an argument name and of a media type.
const (
	maxArgumentTypes = 32
	maxArgumentType  = 128
)

// argumentTypes reads the media types the input schema of a tool gives its
// properties, in contentMediaType as JSON Schema writes it: the format of a
// text argument the client cannot tell from the text, such as a YAML
// definition. A schema the agent cannot read gives none.
func argumentTypes(tool *mcp.Tool) map[string]string {
	data, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return nil
	}
	var schema struct {
		Properties map[string]struct {
			ContentMediaType string `json:"contentMediaType"`
		} `json:"properties"`
	}
	if json.Unmarshal(data, &schema) != nil {
		return nil
	}
	var types map[string]string
	for name, property := range schema.Properties {
		mediaType := property.ContentMediaType
		if mediaType == "" || len(name) > maxArgumentType || len(mediaType) > maxArgumentType || len(types) == maxArgumentTypes {
			continue
		}
		if types == nil {
			types = map[string]string{}
		}
		types[name] = mediaType
	}
	return types
}

func toolTitle(tool *mcp.Tool) string {
	if tool.Title != "" {
		return tool.Title
	}
	if tool.Annotations != nil && tool.Annotations.Title != "" {
		return tool.Annotations.Title
	}
	return tool.Name
}
