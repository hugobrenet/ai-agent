package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opensvc/ai-agent/internal/agent"
	"github.com/opensvc/ai-agent/internal/llm"
)

// slowToolTurn emits a tool start, waits as a tool waiting for an action
// would, then completes.
func slowToolTurn(wait time.Duration) turnFunc {
	return turnFunc(func(ctx context.Context, _ string, emit agent.EmitFunc) error {
		if err := emit(agent.Event{Type: agent.EventToolStarted, ToolName: "list_node_actions", Iteration: 1}); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		if err := emit(agent.Event{Type: agent.EventToolFinished, ToolName: "list_node_actions", Iteration: 1}); err != nil {
			return err
		}
		return emit(agent.Event{Type: agent.EventCompleted, FinishReason: llm.FinishReasonCompleted, Iteration: 2})
	})
}

// streamOverHTTP2 posts a run to handler over TLS and HTTP/2, as the agent
// serves it, and reads the whole stream.
func streamOverHTTP2(t *testing.T, handler http.Handler) string {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	request := newAuthenticatedRunRequest(runBody("follow the action"))
	request.RequestURI = ""
	target, err := http.NewRequest(request.Method, server.URL+request.URL.Path, request.Body)
	if err != nil {
		t.Fatal(err)
	}
	target.Header = request.Header
	response, err := server.Client().Do(target)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 {
		t.Fatalf("protocol = %s, want HTTP/2", response.Proto)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read stream: %v, after %q", err, body)
	}
	return string(body)
}

func TestRunOutlivesTheWriteTimeoutBetweenEvents(t *testing.T) {
	timeout, interval := streamWriteTimeout, streamKeepAliveInterval
	streamWriteTimeout, streamKeepAliveInterval = 200*time.Millisecond, time.Hour
	t.Cleanup(func() { streamWriteTimeout, streamKeepAliveInterval = timeout, interval })
	// A tool running three write timeouts long: the deadline of the last
	// write must not reset the stream.
	body := streamOverHTTP2(t, newTestHandler(t, slowToolTurn(600*time.Millisecond)))
	events := aguiEvents(t, body)
	if len(events) == 0 || events[len(events)-1]["type"] != "RUN_FINISHED" {
		t.Fatalf("events = %+v", events)
	}
}

func TestRunKeepsTheStreamAlive(t *testing.T) {
	interval := streamKeepAliveInterval
	streamKeepAliveInterval = 50 * time.Millisecond
	t.Cleanup(func() { streamKeepAliveInterval = interval })
	body := streamOverHTTP2(t, newTestHandler(t, slowToolTurn(300*time.Millisecond)))
	events := aguiEvents(t, body)
	if !strings.Contains(body, ": keepalive\n\n") || len(events) == 0 || events[len(events)-1]["type"] != "RUN_FINISHED" {
		t.Fatalf("body = %q", body)
	}
}
