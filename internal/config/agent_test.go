package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadAgent(t *testing.T) {
	config, err := loadAgent(func(string) string { return "" })
	if err != nil {
		t.Fatalf("load default agent config: %v", err)
	}
	if config.MaxIterations != DefaultAgentMaxIterations || config.Timeout != DefaultAgentTimeout {
		t.Fatalf("got config %+v, want max iterations %d and timeout %s", config, DefaultAgentMaxIterations, DefaultAgentTimeout)
	}

	config, err = loadAgent(func(key string) string {
		switch key {
		case "OPENSVC_AI_AGENT_MAX_ITERATIONS":
			return "12"
		case "OPENSVC_AI_AGENT_TIMEOUT":
			return "10m"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("load custom agent config: %v", err)
	}
	if config.MaxIterations != 12 || config.Timeout != 10*time.Minute {
		t.Fatalf("got custom config %+v", config)
	}
}

func TestLoadAgentRejectsInvalidTimeout(t *testing.T) {
	for _, value := range []string{"invalid", "0s", "500ms", "30m1s"} {
		_, err := loadAgent(func(key string) string {
			if key == "OPENSVC_AI_AGENT_TIMEOUT" {
				return value
			}
			return ""
		})
		if err == nil || !strings.Contains(err.Error(), "between 1s and 30m0s") {
			t.Fatalf("value %q error = %v", value, err)
		}
	}
}

func TestLoadAgentRejectsInvalidMaxIterations(t *testing.T) {
	for _, value := range []string{"invalid", "0", "33"} {
		_, err := loadAgent(func(string) string { return value })
		if err == nil || !strings.Contains(err.Error(), "between 1") {
			t.Fatalf("value %q error = %v", value, err)
		}
	}
}

func TestLoadAgentToolSearchThreshold(t *testing.T) {
	for value, want := range map[string]int{"": 0, "40": 40, "-1": -1, "1024": 1024} {
		config, err := loadAgent(func(key string) string {
			if key == "OPENSVC_AI_TOOL_SEARCH_THRESHOLD" {
				return value
			}
			return ""
		})
		if err != nil || config.SearchThreshold != want {
			t.Fatalf("threshold %q = %d, %v", value, config.SearchThreshold, err)
		}
	}
	for _, value := range []string{"0", "-2", "1025", "many"} {
		if _, err := loadAgent(func(key string) string {
			if key == "OPENSVC_AI_TOOL_SEARCH_THRESHOLD" {
				return value
			}
			return ""
		}); err == nil {
			t.Fatalf("threshold %q accepted", value)
		}
	}
}

func TestLoadAgentToolSearchScoreRatio(t *testing.T) {
	for value, want := range map[string]float64{"": 0, "0.3": 0.3, "0.2": 0.2, "1": 1, "0": -1} {
		config, err := loadAgent(func(key string) string {
			if key == "OPENSVC_AI_TOOL_SEARCH_SCORE_RATIO" {
				return value
			}
			return ""
		})
		if err != nil || config.SearchScoreRatio != want {
			t.Fatalf("ratio %q = %v, %v", value, config.SearchScoreRatio, err)
		}
	}
	for _, value := range []string{"-0.1", "1.5", "NaN", "many"} {
		if _, err := loadAgent(func(key string) string {
			if key == "OPENSVC_AI_TOOL_SEARCH_SCORE_RATIO" {
				return value
			}
			return ""
		}); err == nil {
			t.Fatalf("ratio %q accepted", value)
		}
	}
}
