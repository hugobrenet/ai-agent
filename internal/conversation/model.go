package conversation

import (
	"encoding/json"
	"time"
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
}
