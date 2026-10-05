package conversation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hugobrenet/opensvc-ai-agent/internal/agent"
	"github.com/hugobrenet/opensvc-ai-agent/internal/llm"
)

func TestMessageQueryBounds(t *testing.T) {
	for _, query := range []MessageQuery{
		{Limit: -1}, {Limit: 101}, {Before: "0:1"}, {Before: "1:0"},
		{Before: "-1:1"}, {Before: "+1:1"}, {Before: "01:1"},
		{Before: "1:1:1"}, {Before: "1"}, {Before: "1: 1"},
		{Before: "9223372036854775808:1"},
	} {
		if _, _, _, err := query.Bounds(); !errors.Is(err, ErrInvalid) {
			t.Errorf("Bounds(%+v) error = %v", query, err)
		}
	}
	limit, turn, message, err := (MessageQuery{Before: "3:4"}).Bounds()
	if err != nil || limit != DefaultMessagePageLimit || turn != 3 || message != 4 {
		t.Fatalf("default bounds = %d, %d, %d, %v", limit, turn, message, err)
	}
}

func TestServiceMessagesChecksOwnershipAndExpiryWithoutRunningTurn(t *testing.T) {
	store := newServiceTestStore()
	service := newTestService(t, store, turnRunnerFunc(func(context.Context, []llm.Message, string, agent.EmitFunc) (agent.TurnResult, error) {
		t.Fatal("reading messages invoked the model")
		return agent.TurnResult{}, nil
	}))
	page, err := service.Messages(t.Context(), serviceTestIdentity(), store.item.ID, MessageQuery{})
	if err != nil || page.Messages == nil || store.messageReads != 1 || store.messageQuery.Limit != DefaultMessagePageLimit {
		t.Fatalf("Messages() = %+v, %v, reads = %d", page, err, store.messageReads)
	}
	for _, field := range []string{"cluster", "issuer", "subject"} {
		identity := serviceTestIdentity()
		switch field {
		case "cluster":
			identity.ClusterID = "other-cluster"
		case "issuer":
			identity.Issuer = "other-issuer"
		case "subject":
			identity.Subject = "other-user"
		}
		if _, err := service.Messages(t.Context(), identity, store.item.ID, MessageQuery{}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign %s messages error = %v", field, err)
		}
	}
	if _, err := service.Messages(t.Context(), serviceTestIdentity(), "missing", MessageQuery{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing messages error = %v", err)
	}
	store.item.ExpiresAt = serviceTestNow.Add(-time.Second)
	if _, err := service.Messages(t.Context(), serviceTestIdentity(), store.item.ID, MessageQuery{}); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired messages error = %v", err)
	}
	if store.messageReads != 1 {
		t.Fatalf("unauthorized/expired requests read messages: %d", store.messageReads)
	}
}
