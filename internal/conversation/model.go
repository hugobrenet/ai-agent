package conversation

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"
	"unicode"

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
	// PromptID is the ID the client gave the message of the user, empty
	// when it gave none.
	PromptID string
	// Runs are the executions of the turn: one, plus one per resumption
	// after a confirmation that ran on a different agent or MCP server.
	Runs []TurnRun
	// Timing are the times of the turn, empty for a turn stored before
	// they were.
	Timing TurnTiming
}

// TurnTiming are the times of a turn on the clock of the agent, in
// milliseconds since the epoch, the ones its stream carried: each run, from
// its claim to its end, the waits for a decision left out; each call of a
// tool, by its ID, from its start to its result, with the arguments it ran
// with when the user edited them; and the tokens of its model calls.
type TurnTiming struct {
	Runs  []TimingRun           `json:"runs,omitempty"`
	Calls map[string]TimingCall `json:"calls,omitempty"`
	Usage *TimingUsage          `json:"usage,omitempty"`
}

type TimingRun struct {
	StartedAt int64 `json:"started_at"`
	EndedAt   int64 `json:"ended_at,omitempty"`
}

type TimingCall struct {
	StartedAt int64           `json:"started_at,omitempty"`
	EndedAt   int64           `json:"ended_at,omitempty"`
	RanWith   json.RawMessage `json:"ran_with,omitempty"`
}

type TimingUsage struct {
	TotalTokens int64 `json:"total_tokens"`
}

const (
	// maxTimingCalls bounds the calls a turn times: 16 a run, at most 32 runs.
	maxTimingCalls = 512
	// maxTimingRanWithBytes bounds the edited arguments a call ran with.
	maxTimingRanWithBytes = 256 << 10
	maxTimingCallIDBytes  = 256
)

// Validate checks the bounds of the times of a turn.
func (t TurnTiming) Validate() error {
	if len(t.Runs) > MaxTurnRuns || len(t.Calls) > maxTimingCalls {
		return fmt.Errorf("%w: turn timing holds too many runs or calls", ErrInvalid)
	}
	for _, run := range t.Runs {
		if run.StartedAt <= 0 || (run.EndedAt != 0 && run.EndedAt < run.StartedAt) {
			return fmt.Errorf("%w: invalid turn run timing", ErrInvalid)
		}
	}
	for id, call := range t.Calls {
		if id == "" || len(id) > maxTimingCallIDBytes || len(call.RanWith) > maxTimingRanWithBytes || call.StartedAt < 0 || call.EndedAt < 0 {
			return fmt.Errorf("%w: invalid tool call timing", ErrInvalid)
		}
	}
	if t.Usage != nil && t.Usage.TotalTokens < 0 {
		return fmt.Errorf("%w: invalid turn usage", ErrInvalid)
	}
	return nil
}

// Empty tells a turn stored without times.
func (t TurnTiming) Empty() bool {
	return len(t.Runs) == 0 && len(t.Calls) == 0 && t.Usage == nil
}

// MaxTurnRuns bounds the executions recorded on a turn.
const MaxTurnRuns = 32

// TurnRun describes what executed a turn, or a part of it until a
// confirmation: the agent and its model, the hash of the system prompt
// actually sent, the MCP server with the hash of its tool catalog, whether
// the catalog was searched and the tools shown to the model.
type TurnRun struct {
	AgentVersion       string `json:"agent_version"`
	Model              string `json:"model"`
	SystemPromptSHA256 string `json:"system_prompt_sha256"`
	MCPServer          string `json:"mcp_server"`
	MCPVersion         string `json:"mcp_version"`
	ToolsSHA256        string `json:"tools_sha256"`
	ToolSearch         bool   `json:"tool_search"`
	// SearchScoreRatio is the share of the best score under which a search
	// result was not shown; 0 when all were.
	SearchScoreRatio float64  `json:"tool_search_score_ratio,omitempty"`
	ShownTools       []string `json:"shown_tools"`
	// ShownToolsOmitted counts the tools shown left out of ShownTools.
	ShownToolsOmitted int `json:"shown_tools_omitted,omitempty"`
	// History describes the history sent to the model.
	History *HistoryStats `json:"history,omitempty"`
}

// Equal tells whether two runs describe the same execution context.
func (r TurnRun) Equal(other TurnRun) bool {
	return r.AgentVersion == other.AgentVersion && r.Model == other.Model && r.SystemPromptSHA256 == other.SystemPromptSHA256 &&
		r.MCPServer == other.MCPServer && r.MCPVersion == other.MCPVersion && r.ToolsSHA256 == other.ToolsSHA256 &&
		r.ToolSearch == other.ToolSearch && r.SearchScoreRatio == other.SearchScoreRatio && slices.Equal(r.ShownTools, other.ShownTools) && r.ShownToolsOmitted == other.ShownToolsOmitted &&
		(r.History == nil) == (other.History == nil) && (r.History == nil || *r.History == *other.History)
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
func (a AgentInfo) run(mcp agent.MCPServer, exposure agent.Exposure, history *HistoryStats) TurnRun {
	return TurnRun{
		History:      history,
		AgentVersion: a.Version, Model: a.Model, SystemPromptSHA256: exposure.SystemPromptSHA256,
		MCPServer: mcp.Name, MCPVersion: mcp.Version, ToolsSHA256: mcp.ToolsSHA256,
		ToolSearch: exposure.ToolSearch, SearchScoreRatio: exposure.SearchScoreRatio, ShownTools: exposure.ShownTools,
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
	// Timing are the times of the turn so far, stored with it.
	Timing TurnTiming
}

// maxPromptIDBytes bounds the ID a client gives the message of the user.
const maxPromptIDBytes = 128

// ValidPromptID tells whether a client gave the message of the user an ID
// the conversation keeps: at most 128 printable bytes without spaces. It is
// a key of display, which nothing else trusts.
func ValidPromptID(id string) bool {
	if id == "" || len(id) > maxPromptIDBytes {
		return false
	}
	for _, r := range id {
		if !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// MessageID names a stored message: <turn ID>:<sequence>, or, for the
// message of the user, the ID the client gave it.
func MessageID(turnID, promptID string, sequence int64) string {
	if sequence == 1 && promptID != "" {
		return promptID
	}
	return turnID + ":" + strconv.FormatInt(sequence, 10)
}
