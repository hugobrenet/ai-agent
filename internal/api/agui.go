package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"github.com/opensvc/ai-agent/internal/agent"
	"github.com/opensvc/ai-agent/internal/agui"
	"github.com/opensvc/ai-agent/internal/auth"
	"github.com/opensvc/ai-agent/internal/buildinfo"
	"github.com/opensvc/ai-agent/internal/conversation"
	"github.com/opensvc/ai-agent/internal/llm"
)

const (
	// maxRunRequestBytes bounds the RunAgentInput of a run: an AG-UI client
	// sends the messages of the thread with each run.
	maxRunRequestBytes = 4 << 20
	maxRunIDBytes      = 128
)

// aguiCodec is the AG-UI protocol of a run: one data line per event.
type aguiCodec struct {
	encoder *agui.Encoder
	// opening are events written right after RUN_STARTED, such as the
	// result of a rejected call.
	opening []agui.Event
}

func (c *aguiCodec) begin() ([]byte, error) {
	return aguiFrames(append(c.encoder.Start(), c.opening...))
}

func (c *aguiCodec) event(event agent.Event) ([]byte, error) {
	events, err := c.encoder.Encode(event)
	if err != nil {
		return nil, err
	}
	return aguiFrames(events)
}

func (c *aguiCodec) failure(code, message string) ([]byte, error) {
	return aguiFrames(c.encoder.Failure(code, message))
}

func aguiFrames(events []agui.Event) ([]byte, error) {
	var frames []byte
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			return nil, fmt.Errorf("encode AG-UI event: %w", err)
		}
		frames = fmt.Appendf(frames, "data: %s\n\n", data)
	}
	return frames, nil
}

// serveConversationRun runs the agent on a conversation for an AG-UI
// client: a new prompt, the last message of the user, or the resume of the
// confirmation the conversation waits on. The stream speaks AG-UI.
func serveConversationRun(service ConversationService, limiter *askLimiter, audit auditLogger) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		id := request.PathValue("id")
		attributes := []slog.Attr{slog.String("conversation_id", boundedAuditID(id))}
		input, status, apiError := decodeRunRequest(response, request, id)
		reject := func(status int, apiError *APIError) {
			audit.event(request.Context(), "conversation_run_rejected", appendAttributes(attributes,
				slog.Int("status", status), slog.String("code", apiError.Code),
			)...)
			writeJSONError(response, status, apiError.Code, apiError.Message)
		}
		if apiError != nil {
			reject(status, apiError)
			return
		}
		attributes = append(attributes, slog.String("run_id", boundedAuditID(input.RunID)))
		if !acquireAgentSlot(response, request, limiter, audit, "conversation_run", attributes) {
			return
		}
		defer limiter.release()
		identity, ok := auth.IdentityFromContext(request.Context())
		if !ok {
			writeUnauthorized(response)
			return
		}
		codec := &aguiCodec{encoder: agui.NewEncoder(input.ThreadID, input.RunID)}
		// The tools of the client are offered to the model in the turn the
		// run runs or resumes.
		tools := make([]agent.ClientTool, 0, len(input.Tools))
		for _, tool := range input.Tools {
			tools = append(tools, agent.ClientTool{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
		}
		if err := agent.ValidateClientTools(tools); err != nil {
			reject(http.StatusBadRequest, &APIError{Code: "invalid_tools", Message: err.Error()})
			return
		}
		runContext := agent.WithClientTools(request.Context(), tools)
		answers, skippedAnswers, err := input.ToolAnswers()
		if err != nil {
			reject(http.StatusBadRequest, &APIError{Code: "invalid_tool_results", Message: err.Error()})
			return
		}
		var execution conversation.TurnExecution
		switch {
		case len(input.Resume) > 0:
			entry := input.Resume[0]
			approved, err := entry.Approval()
			if err != nil {
				reject(http.StatusBadRequest, &APIError{Code: "invalid_decision", Message: err.Error()})
				return
			}
			pending, err := service.PendingConfirmation(request.Context(), identity, id)
			if err != nil {
				writeConversationError(response, request, audit, "conversation_run_rejected", err, id)
				return
			}
			if pending == nil || pending.ConfirmationID != entry.InterruptID {
				reject(http.StatusConflict, &APIError{Code: "confirmation_not_pending", Message: "the conversation waits on no such interrupt"})
				return
			}
			var state agent.SuspendedTurn
			call, err := func() (string, error) {
				if err := json.Unmarshal(pending.State, &state); err != nil {
					return "", err
				}
				call, err := state.PendingCall()
				return call.ID, err
			}()
			if err != nil {
				reject(http.StatusConflict, &APIError{Code: "confirmation_not_pending", Message: "the pending confirmation cannot be resumed"})
				return
			}
			decision := agent.DecisionReject
			if approved {
				decision = agent.DecisionApprove
			}
			attributes = append(attributes, slog.String("turn_id", boundedAuditID(pending.TurnID)), slog.String("decision", string(decision)))
			prepared, err := service.PrepareConfirmation(request.Context(), identity, id, pending.TurnID, pending.ConfirmationID, decision)
			if err != nil {
				status, code, message := conversationError(err)
				reject(status, &APIError{Code: code, Message: message})
				return
			}
			audit.event(request.Context(), "confirmation_decided", attributes...)
			if approved {
				codec.encoder.Resume(call)
			} else {
				codec.opening = codec.encoder.Rejected(call)
			}
			execution = prepared
		case len(answers) > 0:
			// The answers of the client to the calls of its tools resume
			// the turn waiting on them. The client keeps in its messages the
			// results the agent gave in the stream: only the answers to the
			// calls waiting are read.
			pending, err := service.PendingClientCalls(request.Context(), identity, id)
			if err != nil {
				writeConversationError(response, request, audit, "conversation_run_rejected", err, id)
				return
			}
			if len(pending) == 0 {
				reject(http.StatusConflict, &APIError{Code: "client_tools_not_pending", Message: "the conversation waits on no call of a tool of the client"})
				return
			}
			results := make([]llm.ToolResult, 0, len(pending))
			for _, answer := range answers {
				if slices.Contains(pending, answer.CallID) {
					results = append(results, agent.ClientToolResult(answer.CallID, answer.Text, answer.IsError))
				}
			}
			prepared, err := service.PrepareClientResults(request.Context(), identity, id, results)
			if err != nil {
				status, code, message := conversationError(err)
				switch {
				case errors.Is(err, conversation.ErrNotPending):
					status, code, message = http.StatusConflict, "client_tools_not_pending", "the conversation waits on no call of a tool of the client"
				case errors.Is(err, conversation.ErrConfirmationExpired):
					// The wait on the answers shares the lifetime of the
					// confirmations, not their meaning.
					status, code, message = http.StatusGone, "client_tools_expired", "the wait on the answers to the calls of the tools of the client expired; the turn failed"
				}
				reject(status, &APIError{Code: code, Message: message})
				return
			}
			audit.event(request.Context(), "client_tools_answered", appendAttributes(attributes, slog.Int("answers", len(results)))...)
			if len(skippedAnswers) > 0 {
				codec.opening = codec.encoder.Warning("the agent reads the text parts of a tool message only: the other parts were skipped", skippedAnswers)
			}
			execution = prepared
		default:
			prompt, skipped, err := input.Prompt()
			if err != nil {
				reject(http.StatusBadRequest, &APIError{Code: "invalid_prompt", Message: err.Error()})
				return
			}
			if strings.TrimSpace(prompt) == "" {
				reject(http.StatusBadRequest, &APIError{Code: "invalid_prompt", Message: "the message of the user must not be empty"})
				return
			}
			if len(prompt) > maxPromptBytes {
				reject(http.StatusRequestEntityTooLarge, &APIError{Code: "prompt_too_large", Message: "the message of the user is too large"})
				return
			}
			if len(skipped) > 0 {
				codec.opening = codec.encoder.Warning("the agent reads the text parts of a message only: the other parts were skipped", skipped)
			}
			// A new prompt abandons the calls of tools of the client the
			// conversation waits on; a confirmation keeps it busy.
			if err := service.AbandonClientCalls(request.Context(), identity, id); err != nil {
				writeConversationError(response, request, audit, "conversation_run_rejected", err, id)
				return
			}
			prepared, err := service.PrepareTurn(request.Context(), identity, id, prompt)
			if err != nil {
				writeConversationError(response, request, audit, "conversation_run_rejected", err, id)
				return
			}
			execution = prepared
		}
		started := streamAgentWith(response, request, audit, "conversation_run", attributes, codec, func(emit agent.EmitFunc) error {
			return execution.Run(runContext, emit)
		})
		if !started {
			_ = execution.Cancel("streaming_unavailable")
		}
	}
}

// decodeRunRequest reads the RunAgentInput of a run on the conversation id.
// Fields the agent does not read are accepted: AG-UI clients send more than
// the agent reads, and the protocol grows.
func decodeRunRequest(response http.ResponseWriter, request *http.Request, id string) (agui.RunAgentInput, int, *APIError) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return agui.RunAgentInput{}, http.StatusUnsupportedMediaType, &APIError{Code: "unsupported_media_type", Message: "Content-Type must be application/json"}
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxRunRequestBytes)
	decoder := json.NewDecoder(request.Body)
	var input agui.RunAgentInput
	if err := decoder.Decode(&input); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return input, http.StatusRequestEntityTooLarge, &APIError{Code: "request_too_large", Message: "request body is too large"}
		}
		return input, http.StatusBadRequest, &APIError{Code: "invalid_request", Message: "request body must be an AG-UI RunAgentInput"}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return input, http.StatusBadRequest, &APIError{Code: "invalid_request", Message: "request body must contain one JSON object"}
	}
	if input.ThreadID != id {
		return input, http.StatusBadRequest, &APIError{Code: "thread_mismatch", Message: "threadId must be the ID of the conversation of the route"}
	}
	if input.RunID == "" || len(input.RunID) > maxRunIDBytes || strings.ContainsFunc(input.RunID, func(r rune) bool { return !unicode.IsPrint(r) || unicode.IsSpace(r) }) {
		return input, http.StatusBadRequest, &APIError{Code: "invalid_run_id", Message: fmt.Sprintf("runId must be at most %d printable bytes without spaces", maxRunIDBytes)}
	}
	if len(input.Resume) > 1 {
		return input, http.StatusBadRequest, &APIError{Code: "invalid_decision", Message: "a conversation waits on one interrupt at most"}
	}
	return input, 0, nil
}

// agentCapabilities is the AG-UI AgentCapabilities the runs route honours:
// it streams over SSE, calls the tools of its MCP and accepts the tools of
// the client, and pauses for approvals with interrupts, without edits of
// the arguments.
var agentCapabilities = map[string]any{
	"identity":       map[string]any{"name": "OpenSVC AI agent", "type": "opensvc-ai-agent", "provider": "OpenSVC", "version": buildinfo.Version()},
	"transport":      map[string]any{"streaming": true},
	"tools":          map[string]any{"supported": true, "clientProvided": true},
	"humanInTheLoop": map[string]any{"supported": true, "approvals": true, "interrupts": true, "approveWithEdits": false},
	"multimodal":     map[string]any{"input": map[string]any{"image": false, "audio": false, "video": false, "pdf": false, "file": false}},
}

// serveCapabilities answers what the runs route of the backend can do, for
// an AG-UI client to shape its interface before a run.
func serveCapabilities(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, agentCapabilities)
}
