package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/opensvc/ai-agent/internal/agent"
)

const (
	maxAskRequestBytes = 64 << 10
	maxPromptBytes     = 32 << 10
	maxAskStreamBytes  = 16 << 20
)

var (
	// askWriteTimeout bounds each write of the stream, so that a client
	// reading it slowly is cut off; it is cleared between writes, a tool or
	// a model request may run longer without any.
	askWriteTimeout = 15 * time.Second
	// askKeepAliveInterval is how often the stream sends a comment while
	// nothing else is written, which keeps idle proxies from closing it.
	askKeepAliveInterval = 10 * time.Second
)

type AskRequest struct {
	Prompt string `json:"prompt"`
}

type AskUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

type AskEvent struct {
	Type         string           `json:"type"`
	Iteration    int              `json:"iteration,omitempty"`
	TurnID       string           `json:"turn_id,omitempty"`
	TextDelta    string           `json:"text_delta,omitempty"`
	ToolName     string           `json:"tool_name,omitempty"`
	ToolError    *bool            `json:"tool_error,omitempty"`
	Usage        *AskUsage        `json:"usage,omitempty"`
	FinishReason string           `json:"finish_reason,omitempty"`
	Confirmation *AskConfirmation `json:"confirmation,omitempty"`
	Code         string           `json:"code,omitempty"`
	Message      string           `json:"message,omitempty"`
}

// AskConfirmation is a tool call waiting for the decision of the user: the
// exact call that runs if the user approves it.
type AskConfirmation struct {
	ID        string              `json:"id"`
	TurnID    string              `json:"turn_id"`
	Tool      AskConfirmationTool `json:"tool"`
	Arguments json.RawMessage     `json:"arguments"`
	// ArgumentTypes are the media types of text arguments, such as
	// {"yaml": "application/yaml"}, as the input schema of the tool
	// declares them: the client shows them by it, not by guessing.
	ArgumentTypes map[string]string `json:"argument_types,omitempty"`
	ExpiresAt     time.Time         `json:"expires_at"`
}

type AskConfirmationTool struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Destructive bool   `json:"destructive"`
}

type ErrorResponse struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func acquireAgentSlot(response http.ResponseWriter, request *http.Request, limiter *askLimiter, audit auditLogger, prefix string, attributes []slog.Attr) bool {
	if limiter.tryAcquire() {
		return true
	}
	attributes = append(append([]slog.Attr(nil), attributes...),
		slog.Int("status", http.StatusTooManyRequests),
		slog.String("code", "too_many_requests"),
	)
	audit.event(request.Context(), prefix+"_rejected", attributes...)
	response.Header().Set("Retry-After", "1")
	writeJSONError(response, http.StatusTooManyRequests, "too_many_requests", "too many agent requests are already running")
	return false
}

// streamCodec writes the events of the agent in the protocol of a stream:
// the frames opening it, those of each event, and the frame of a failure.
type streamCodec interface {
	begin() ([]byte, error)
	event(agent.Event) ([]byte, error)
	failure(code, message string) ([]byte, error)
}

// askCodec is the SSE protocol of the turns: one named event per event of
// the agent.
type askCodec struct{}

func (askCodec) begin() ([]byte, error) { return nil, nil }

func (askCodec) event(event agent.Event) ([]byte, error) { return askFrame(newAskEvent(event)) }

func (askCodec) failure(code, message string) ([]byte, error) {
	return askFrame(AskEvent{Type: "error", Code: code, Message: message})
}

func askFrame(event AskEvent) ([]byte, error) {
	data, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("encode API stream event: %w", err)
	}
	return fmt.Appendf(nil, "event: %s\ndata: %s\n\n", event.Type, data), nil
}

func streamAgent(response http.ResponseWriter, request *http.Request, audit auditLogger, prefix string, baseAttributes []slog.Attr, run func(agent.EmitFunc) error) bool {
	return streamAgentWith(response, request, audit, prefix, baseAttributes, askCodec{}, run)
}

func streamAgentWith(response http.ResponseWriter, request *http.Request, audit auditLogger, prefix string, baseAttributes []slog.Attr, codec streamCodec, run func(agent.EmitFunc) error) bool {
	flusher, ok := response.(http.Flusher)
	if !ok {
		audit.event(request.Context(), prefix+"_rejected", appendAttributes(baseAttributes,
			slog.Int("status", http.StatusInternalServerError),
			slog.String("code", "streaming_unavailable"),
		)...)
		writeJSONError(response, http.StatusInternalServerError, "streaming_unavailable", "response streaming is unavailable")
		return false
	}
	if err := setAskWriteDeadline(response); err != nil {
		audit.event(request.Context(), prefix+"_rejected", appendAttributes(baseAttributes,
			slog.Int("status", http.StatusInternalServerError),
			slog.String("code", "streaming_unavailable"),
		)...)
		writeJSONError(response, http.StatusInternalServerError, "streaming_unavailable", "response streaming is unavailable")
		return false
	}
	defer clearAskWriteDeadline(response)

	response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(http.StatusOK)
	flusher.Flush()
	clearAskWriteDeadline(response)
	startedAt := time.Now()
	audit.event(request.Context(), prefix+"_started", appendAttributes(baseAttributes, slog.Int("status", http.StatusOK))...)
	opening, openErr := codec.begin()

	writeFailed := false
	completed := false
	finishReason := ""
	lastIteration := 0
	toolCalls := 0
	var usage AskUsage
	var activeToolName string
	var activeToolStartedAt time.Time
	var streamBytes int64
	// The writes of the events and of the keep-alive comments are
	// serialized, each under its own deadline: an HTTP/2 stream whose write
	// deadline passes is reset, however long ago it was written.
	var writeLock sync.Mutex
	write := func(frame func() error) error {
		writeLock.Lock()
		defer writeLock.Unlock()
		if err := setAskWriteDeadline(response); err != nil {
			return err
		}
		defer clearAskWriteDeadline(response)
		return frame()
	}
	stopKeepAlive := make(chan struct{})
	var keepAlive sync.WaitGroup
	keepAlive.Add(1)
	go func() {
		defer keepAlive.Done()
		ticker := time.NewTicker(askKeepAliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopKeepAlive:
				return
			case <-request.Context().Done():
				return
			case <-ticker.C:
				if err := write(func() error { return writeSSEComment(response, flusher, &streamBytes) }); err != nil {
					return
				}
			}
		}
	}()
	recordEvent := func(event agent.Event) {
		switch event.Type {
		case agent.EventToolStarted:
			toolCalls++
			activeToolName = event.ToolName
			activeToolStartedAt = time.Now()
			audit.event(request.Context(), "tool_started",
				slog.String("tool_name", event.ToolName),
				slog.Int("iteration", event.Iteration),
			)
		case agent.EventToolFinished:
			duration := int64(0)
			if activeToolName == event.ToolName && !activeToolStartedAt.IsZero() {
				duration = time.Since(activeToolStartedAt).Milliseconds()
			}
			attributes := []slog.Attr{
				slog.String("tool_name", event.ToolName),
				slog.Int("iteration", event.Iteration),
				slog.Bool("tool_error", event.ToolError),
				slog.Int64("duration_ms", duration),
			}
			if event.ToolError {
				attributes = append(attributes, slog.String("code", "tool_error"))
			}
			audit.event(request.Context(), "tool_finished", attributes...)
			activeToolName = ""
			activeToolStartedAt = time.Time{}
		case agent.EventUsage:
			usage.InputTokens = saturatingAdd(usage.InputTokens, event.Usage.InputTokens)
			usage.OutputTokens = saturatingAdd(usage.OutputTokens, event.Usage.OutputTokens)
			usage.TotalTokens = saturatingAdd(usage.TotalTokens, event.Usage.TotalTokens)
			audit.event(request.Context(), "llm_usage",
				slog.Int("iteration", event.Iteration),
				slog.Int64("input_tokens", event.Usage.InputTokens),
				slog.Int64("output_tokens", event.Usage.OutputTokens),
				slog.Int64("total_tokens", event.Usage.TotalTokens),
			)
		case agent.EventCompleted:
			completed = true
			finishReason = string(event.FinishReason)
		case agent.EventConfirmationRequired:
			// The turn ends here, stored, until the user decides.
			completed = true
			finishReason = string(agent.EventConfirmationRequired)
			audit.event(request.Context(), "confirmation_requested",
				slog.String("tool_name", event.Confirmation.ToolName),
				slog.String("turn_id", boundedAuditID(event.Confirmation.TurnID)),
				slog.Int("iteration", event.Iteration),
			)
		}
	}
	err := openErr
	if err == nil && len(opening) > 0 {
		if err = write(func() error { return writeFrame(response, flusher, &streamBytes, opening) }); err != nil {
			writeFailed = true
		}
	}
	if err == nil {
		err = run(func(event agent.Event) error {
			lastIteration = max(lastIteration, event.Iteration)
			if event.Type != agent.EventToolStarted {
				recordEvent(event)
			}
			frame, err := codec.event(event)
			if err != nil {
				return err
			}
			if len(frame) > 0 {
				if err := write(func() error { return writeFrame(response, flusher, &streamBytes, frame) }); err != nil {
					writeFailed = true
					return err
				}
			}
			if event.Type == agent.EventToolStarted {
				recordEvent(event)
			}
			return nil
		})
	}
	close(stopKeepAlive)
	keepAlive.Wait()
	failureCode := askFailureCode(err, completed, writeFailed, request.Context().Err())
	if activeToolName != "" && failureCode == "" {
		failureCode = "agent_incomplete"
	}
	if activeToolName != "" {
		audit.event(request.Context(), "tool_finished",
			slog.String("tool_name", activeToolName),
			slog.Int("iteration", lastIteration),
			slog.Bool("tool_error", true),
			slog.Int64("duration_ms", time.Since(activeToolStartedAt).Milliseconds()),
			slog.String("code", failureCode),
		)
	}
	terminalAttributes := []slog.Attr{
		slog.Int64("duration_ms", time.Since(startedAt).Milliseconds()),
		slog.Int("iterations", lastIteration),
		slog.Int("tool_calls", toolCalls),
		slog.Int64("input_tokens", usage.InputTokens),
		slog.Int64("output_tokens", usage.OutputTokens),
		slog.Int64("total_tokens", usage.TotalTokens),
	}
	if failureCode == "" {
		terminalAttributes = append(terminalAttributes, slog.String("finish_reason", finishReason))
		audit.event(request.Context(), prefix+"_completed", appendAttributes(baseAttributes, terminalAttributes...)...)
	} else {
		terminalAttributes = append(terminalAttributes, slog.String("code", failureCode))
		audit.event(request.Context(), prefix+"_failed", appendAttributes(baseAttributes, terminalAttributes...)...)
	}
	if err != nil && !completed && !writeFailed && request.Context().Err() == nil {
		code := "agent_failed"
		message := "the agent could not complete the request"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "request_timeout"
			message = "the agent request timed out"
		} else if errors.Is(err, agent.ErrToolCallLimit) {
			code = "tool_call_limit"
			message = "the question needs more tool calls than the agent runs in one answer; ask a narrower question"
		}
		if frame, err := codec.failure(code, message); err == nil && len(frame) > 0 {
			_ = write(func() error { return writeFrame(response, flusher, &streamBytes, frame) })
		}
	}
	return true
}

func appendAttributes(base []slog.Attr, extra ...slog.Attr) []slog.Attr {
	attributes := make([]slog.Attr, 0, len(base)+len(extra))
	attributes = append(attributes, base...)
	return append(attributes, extra...)
}

func askFailureCode(err error, completed bool, writeFailed bool, contextErr error) string {
	if writeFailed {
		return "stream_write_failed"
	}
	if errors.Is(contextErr, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "request_timeout"
	}
	if contextErr != nil || errors.Is(err, context.Canceled) {
		return "request_canceled"
	}
	if err != nil {
		if completed {
			return "agent_cleanup_failed"
		}
		if errors.Is(err, agent.ErrToolCallLimit) {
			return "tool_call_limit"
		}
		return "agent_failed"
	}
	if !completed {
		return "agent_incomplete"
	}
	return ""
}

func saturatingAdd(current int64, value int64) int64 {
	if value < 0 {
		return current
	}
	if value > math.MaxInt64-current {
		return math.MaxInt64
	}
	return current + value
}

func setAskWriteDeadline(response http.ResponseWriter) error {
	err := http.NewResponseController(response).SetWriteDeadline(time.Now().Add(askWriteTimeout))
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("set API stream write deadline: %w", err)
	}
	return nil
}

func clearAskWriteDeadline(response http.ResponseWriter) {
	_ = http.NewResponseController(response).SetWriteDeadline(time.Time{})
}

func decodeAskRequest(response http.ResponseWriter, request *http.Request) (AskRequest, int, *APIError) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return AskRequest{}, http.StatusUnsupportedMediaType, &APIError{Code: "unsupported_media_type", Message: "Content-Type must be application/json"}
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxAskRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var askRequest AskRequest
	if err := decoder.Decode(&askRequest); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return AskRequest{}, http.StatusRequestEntityTooLarge, &APIError{Code: "request_too_large", Message: "request body is too large"}
		}
		return AskRequest{}, http.StatusBadRequest, &APIError{Code: "invalid_request", Message: "request body must be a JSON object containing a prompt"}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return AskRequest{}, http.StatusBadRequest, &APIError{Code: "invalid_request", Message: "request body must contain one JSON object"}
	}
	if strings.TrimSpace(askRequest.Prompt) == "" {
		return AskRequest{}, http.StatusBadRequest, &APIError{Code: "invalid_prompt", Message: "prompt must not be empty"}
	}
	if len(askRequest.Prompt) > maxPromptBytes {
		return AskRequest{}, http.StatusRequestEntityTooLarge, &APIError{Code: "prompt_too_large", Message: "prompt is too large"}
	}
	return askRequest, 0, nil
}

func newAskEvent(event agent.Event) AskEvent {
	streamEvent := AskEvent{Type: string(event.Type), Iteration: event.Iteration}
	switch event.Type {
	case agent.EventTextDelta:
		streamEvent.TextDelta = event.TextDelta
	case agent.EventToolStarted:
		streamEvent.ToolName = event.ToolName
	case agent.EventToolFinished:
		streamEvent.ToolName = event.ToolName
		toolError := event.ToolError
		streamEvent.ToolError = &toolError
	case agent.EventUsage:
		streamEvent.Usage = &AskUsage{
			InputTokens:  event.Usage.InputTokens,
			OutputTokens: event.Usage.OutputTokens,
			TotalTokens:  event.Usage.TotalTokens,
		}
	case agent.EventCompleted:
		streamEvent.FinishReason = string(event.FinishReason)
		streamEvent.TurnID = event.TurnID
	case agent.EventConfirmationRequired:
		c := event.Confirmation
		streamEvent.Confirmation = &AskConfirmation{
			ID: c.ID, TurnID: c.TurnID, Arguments: c.Arguments, ArgumentTypes: c.ArgumentTypes, ExpiresAt: c.ExpiresAt,
			Tool: AskConfirmationTool{Name: c.ToolName, Title: c.ToolTitle, Destructive: c.Destructive},
		}
	}
	return streamEvent
}

// writeSSEComment writes a comment line, which SSE clients ignore.
func writeSSEComment(response io.Writer, flusher http.Flusher, streamBytes *int64) error {
	frame := []byte(": keepalive\n\n")
	if *streamBytes+int64(len(frame)) > maxAskStreamBytes {
		return fmt.Errorf("API stream exceeds %d bytes", maxAskStreamBytes)
	}
	written, err := response.Write(frame)
	*streamBytes += int64(written)
	if err != nil {
		return fmt.Errorf("write API stream comment: %w", err)
	}
	flusher.Flush()
	return nil
}

// writeFrame writes frames of the stream, within its size.
func writeFrame(response io.Writer, flusher http.Flusher, streamBytes *int64, frame []byte) error {
	if *streamBytes+int64(len(frame)) > maxAskStreamBytes {
		return fmt.Errorf("API stream exceeds %d bytes", maxAskStreamBytes)
	}
	written, err := response.Write(frame)
	*streamBytes += int64(written)
	if err != nil {
		return fmt.Errorf("write API stream event: %w", err)
	}
	if written != len(frame) {
		return fmt.Errorf("write API stream event: %w", io.ErrShortWrite)
	}
	flusher.Flush()
	return nil
}

func writeJSONError(response http.ResponseWriter, status int, code string, message string) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(ErrorResponse{Error: APIError{Code: code, Message: message}})
}
