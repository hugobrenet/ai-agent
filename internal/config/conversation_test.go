package config

import (
	"testing"
	"time"
)

func TestLoadConversation(t *testing.T) {
	config, err := loadConversation(func(key string) string {
		switch key {
		case "OPENSVC_AI_CONVERSATION_DB_PATH":
			return "/tmp/agent/conversations.db"
		case "OPENSVC_AI_CONVERSATION_LIFETIME":
			return "72h"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatalf("load conversation config: %v", err)
	}
	if config.DatabasePath != "/tmp/agent/conversations.db" || config.Lifetime != 72*time.Hour {
		t.Fatalf("unexpected conversation config %+v", config)
	}
}

func TestLoadConversationRejectsInvalidLifetime(t *testing.T) {
	for _, value := range []string{"invalid", "30m", "8761h"} {
		if _, err := loadConversation(func(key string) string {
			if key == "OPENSVC_AI_CONVERSATION_LIFETIME" {
				return value
			}
			return ""
		}); err == nil {
			t.Fatalf("lifetime %q succeeded", value)
		}
	}
}

func TestLoadConversationFeedbackRetention(t *testing.T) {
	config, err := loadConversation(func(string) string { return "" })
	if err != nil || config.FeedbackRetention != 180*24*time.Hour {
		t.Fatalf("default retention = %v, %v", config.FeedbackRetention, err)
	}
	config, err = loadConversation(func(key string) string {
		if key == "OPENSVC_AI_FEEDBACK_RETENTION" {
			return "720h"
		}
		return ""
	})
	if err != nil || config.FeedbackRetention != 720*time.Hour {
		t.Fatalf("retention = %v, %v", config.FeedbackRetention, err)
	}
	for _, value := range []string{"invalid", "23h", "26281h"} {
		if _, err := loadConversation(func(key string) string {
			if key == "OPENSVC_AI_FEEDBACK_RETENTION" {
				return value
			}
			return ""
		}); err == nil {
			t.Fatalf("retention %q succeeded", value)
		}
	}
}
