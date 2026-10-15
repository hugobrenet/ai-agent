package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAgentMaxIterations = 8
	maximumAgentMaxIterations = 32
	DefaultAgentTimeout       = 5 * time.Minute
	minimumAgentTimeout       = time.Second
	maximumAgentTimeout       = 30 * time.Minute
	maximumSearchThreshold    = 1024
)

type AgentConfig struct {
	MaxIterations int
	Timeout       time.Duration
	// SearchThreshold is the largest MCP catalog shown whole to the model;
	// 0 keeps the default of the agent, a negative value never searches.
	SearchThreshold int
}

func LoadAgent() (AgentConfig, error) {
	return loadAgent(os.Getenv)
}

func loadAgent(getenv func(string) string) (AgentConfig, error) {
	config := AgentConfig{MaxIterations: DefaultAgentMaxIterations, Timeout: DefaultAgentTimeout}
	if value := strings.TrimSpace(getenv("OPENSVC_AI_AGENT_MAX_ITERATIONS")); value != "" {
		maxIterations, err := strconv.Atoi(value)
		if err != nil || maxIterations <= 0 || maxIterations > maximumAgentMaxIterations {
			return AgentConfig{}, fmt.Errorf("parse OPENSVC_AI_AGENT_MAX_ITERATIONS %q: expected an integer between 1 and %d", value, maximumAgentMaxIterations)
		}
		config.MaxIterations = maxIterations
	}
	if value := strings.TrimSpace(getenv("OPENSVC_AI_AGENT_TIMEOUT")); value != "" {
		timeout, err := time.ParseDuration(value)
		if err != nil || timeout < minimumAgentTimeout || timeout > maximumAgentTimeout {
			return AgentConfig{}, fmt.Errorf(
				"parse OPENSVC_AI_AGENT_TIMEOUT %q: expected a duration between %s and %s",
				value,
				minimumAgentTimeout,
				maximumAgentTimeout,
			)
		}
		config.Timeout = timeout
	}
	if value := strings.TrimSpace(getenv("OPENSVC_AI_TOOL_SEARCH_THRESHOLD")); value != "" {
		threshold, err := strconv.Atoi(value)
		if err != nil || threshold < -1 || threshold == 0 || threshold > maximumSearchThreshold {
			return AgentConfig{}, fmt.Errorf("parse OPENSVC_AI_TOOL_SEARCH_THRESHOLD %q: expected an integer between 1 and %d, or -1 to never search", value, maximumSearchThreshold)
		}
		config.SearchThreshold = threshold
	}
	return config, nil
}
