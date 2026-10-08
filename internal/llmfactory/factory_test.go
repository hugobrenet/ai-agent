package llmfactory

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/opensvc/ai-agent/internal/config"
	"github.com/opensvc/ai-agent/internal/llm/chatcompletions"
	"github.com/opensvc/ai-agent/internal/llm/messages"
	"github.com/opensvc/ai-agent/internal/llm/responses"
)

func TestNewSelectsResponsesProtocol(t *testing.T) {
	client, err := New(config.LLMConfig{
		Protocol:        config.LLMProtocolResponses,
		BaseURL:         "https://llm.example.test/v1",
		Model:           "test-model",
		AuthMode:        config.LLMAuthModeNone,
		Timeout:         time.Minute,
		MaxOutputTokens: 1024,
	}, http.DefaultClient)
	if err != nil {
		t.Fatalf("create LLM client: %v", err)
	}
	if _, ok := client.(*responses.Client); !ok {
		t.Fatalf("got client type %T, want Responses client", client)
	}
}

func TestNewSelectsChatCompletionsProtocol(t *testing.T) {
	client, err := New(config.LLMConfig{
		Protocol:        config.LLMProtocolChatCompletions,
		BaseURL:         "https://llm.example.test/v1",
		Model:           "test-model",
		AuthMode:        config.LLMAuthModeNone,
		Timeout:         time.Minute,
		MaxOutputTokens: 1024,
	}, http.DefaultClient)
	if err != nil {
		t.Fatalf("create LLM client: %v", err)
	}
	if _, ok := client.(*chatcompletions.Client); !ok {
		t.Fatalf("got client type %T, want Chat Completions client", client)
	}
}

func TestNewRejectsUnsupportedProtocol(t *testing.T) {
	_, err := New(config.LLMConfig{Protocol: "unknown"}, nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("New() error = %v, want unsupported protocol", err)
	}
}

func TestNewSelectsMessagesProtocol(t *testing.T) {
	t.Setenv(config.LLMAPITokenEnv, "test-secret")
	for _, mode := range []string{config.LLMAuthModeAPIKey, config.LLMAuthModeBearer, config.LLMAuthModeNone} {
		client, err := New(config.LLMConfig{
			Protocol: config.LLMProtocolMessages, BaseURL: "https://api.anthropic.com/v1", Model: "test-model",
			AuthMode: mode, APITokenEnv: config.LLMAPITokenEnv, Timeout: time.Minute, MaxOutputTokens: 1024,
		}, http.DefaultClient)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := client.(*messages.Client); !ok {
			t.Fatalf("got client type %T, want Messages client", client)
		}
	}
}

func TestEnvironmentTokenSource(t *testing.T) {
	const name = "OPENSVC_AI_TEST_TOKEN_SOURCE"
	t.Setenv(name, "placeholder")
	token, err := environmentTokenSource(name)()
	if err != nil {
		t.Fatalf("load environment token: %v", err)
	}
	if token != "placeholder" {
		t.Fatalf("got token %q", token)
	}

	t.Setenv(name, "")
	if _, err := environmentTokenSource(name)(); err == nil {
		t.Fatal("empty environment token accepted")
	}
}
