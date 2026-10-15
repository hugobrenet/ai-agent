package conversation

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/opensvc/ai-agent/internal/agent"
)

// Owner binds a conversation to the backend it was created on and to the
// verified identity of its user. A conversation is never visible from
// another backend. ClusterID is empty for a backend whose identities are not
// bound to a cluster.
type Owner struct {
	Backend   string
	ClusterID string
	Issuer    string
	Subject   string
}

const maxBackendNameBytes = 32

// ValidBackendName accepts the short lowercase name of a backend, such as
// om3, which also names its API routes.
func ValidBackendName(name string) bool {
	if name == "" || len(name) > maxBackendNameBytes {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

type Conversation struct {
	ID          string
	Title       string
	Owner       Owner
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ExpiresAt   time.Time
	StoredBytes int64
}

type TurnStatus string

const (
	TurnRunning TurnStatus = "running"
	// TurnAwaitingConfirmation is a turn suspended until the user confirms
	// or rejects the tool call it waits on.
	TurnAwaitingConfirmation TurnStatus = "awaiting_confirmation"
	TurnCompleted            TurnStatus = "completed"
	TurnFailed               TurnStatus = "failed"
	TurnCanceled             TurnStatus = "canceled"
	TurnInterrupted          TurnStatus = "interrupted"
)

type Turn struct {
	ID             string
	ConversationID string
	Sequence       int64
	Status         TurnStatus
	ErrorCode      string
	StartedAt      time.Time
	CompletedAt    *time.Time
	// Runs are the executions of the turn: one, plus one per resumption
	// after a confirmation that ran on a different agent or MCP server.
	Runs []TurnRun
}

// MaxTurnRuns bounds the executions recorded on a turn.
const MaxTurnRuns = 32

// TurnRun describes what executed a turn, or a part of it until a
// confirmation: the agent and its model, the hash of the system prompt
// actually sent, the MCP server with the hash of its tool catalog, whether
// the catalog was searched and the tools shown to the model.
type TurnRun struct {
	AgentVersion       string   `json:"agent_version"`
	Model              string   `json:"model"`
	SystemPromptSHA256 string   `json:"system_prompt_sha256"`
	MCPServer          string   `json:"mcp_server"`
	MCPVersion         string   `json:"mcp_version"`
	ToolsSHA256        string   `json:"tools_sha256"`
	ToolSearch         bool     `json:"tool_search"`
	ShownTools         []string `json:"shown_tools"`
	// ShownToolsOmitted counts the tools shown left out of ShownTools.
	ShownToolsOmitted int `json:"shown_tools_omitted,omitempty"`
}

// Equal tells whether two runs describe the same execution context.
func (r TurnRun) Equal(other TurnRun) bool {
	return r.AgentVersion == other.AgentVersion && r.Model == other.Model && r.SystemPromptSHA256 == other.SystemPromptSHA256 &&
		r.MCPServer == other.MCPServer && r.MCPVersion == other.MCPVersion && r.ToolsSHA256 == other.ToolsSHA256 &&
		r.ToolSearch == other.ToolSearch && slices.Equal(r.ShownTools, other.ShownTools) && r.ShownToolsOmitted == other.ShownToolsOmitted
}

// AgentInfo describes the agent of a backend: its version and its model.
type AgentInfo struct {
	Version string
	Model   string
}

// NewAgentInfo describes the agent of a backend.
func NewAgentInfo(version string, model string) AgentInfo {
	return AgentInfo{Version: version, Model: model}
}

// run describes an execution of a turn by this agent, as the agent reported
// it: the MCP server and what the model was given.
func (a AgentInfo) run(mcp agent.MCPServer, exposure agent.Exposure) TurnRun {
	return TurnRun{
		AgentVersion: a.Version, Model: a.Model, SystemPromptSHA256: exposure.SystemPromptSHA256,
		MCPServer: mcp.Name, MCPVersion: mcp.Version, ToolsSHA256: mcp.ToolsSHA256,
		ToolSearch: exposure.ToolSearch, ShownTools: exposure.ShownTools,
	}
}

// PendingConfirmation is the tool call a suspended turn waits on, with the
// encoded agent state the turn resumes from.
type PendingConfirmation struct {
	ConversationID string
	TurnID         string
	ConfirmationID string
	ToolName       string
	ToolTitle      string
	Arguments      json.RawMessage
	ArgumentsHash  string
	State          []byte
	CreatedAt      time.Time
	ExpiresAt      time.Time
	// Run describes the execution that suspended the turn; it is added to
	// the runs of the turn.
	Run TurnRun
}
