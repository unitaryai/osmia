// Package claudecode provides the Claude Code execution engine for RoboDev.
// This file implements the agent teams configuration for Claude Code.
// Agent teams are deprecated; use subagents.go and the SubAgent type instead.
package claudecode

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// AgentDef defines a single agent's role and model within a team.
type AgentDef struct {
	// Role describes what the agent is responsible for.
	Role string `json:"role" yaml:"role"`

	// Model is the optional model to use for this agent (e.g. "opus", "haiku").
	Model string `json:"model,omitempty" yaml:"model,omitempty"`

	// Instructions provides optional additional instructions for the agent.
	Instructions string `json:"instructions,omitempty" yaml:"instructions,omitempty"`
}

// TeamsConfig configures the experimental agent teams feature for Claude Code.
// Agent teams allow splitting a task across multiple Claude Code instances
// running in-process within a single K8s Job pod.
//
// Deprecated: use SubAgent and WithSubAgents instead.
type TeamsConfig struct {
	// Enabled controls whether agent teams mode is active. Defaults to false.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Mode specifies the teams execution mode. Currently only "in-process"
	// is supported (no tmux required inside containers).
	Mode string `json:"mode" yaml:"mode"`

	// MaxTeammates is the maximum number of teammate agents that can be
	// spawned within a single job. Defaults to 3.
	MaxTeammates int `json:"max_teammates" yaml:"max_teammates"`

	// Agents defines the named agents available in the team. When empty,
	// default agents are generated based on the task type.
	Agents map[string]AgentDef `json:"agents,omitempty" yaml:"agents,omitempty"`
}

// DefaultTeamsConfig returns the default agent teams configuration.
func DefaultTeamsConfig() TeamsConfig {
	return TeamsConfig{
		Enabled:      false,
		Mode:         "in-process",
		MaxTeammates: 3,
	}
}

// agentFlagEntry is the JSON structure for a single agent in the --agents flag.
type agentFlagEntry struct {
	Name  string `json:"name"`
	Role  string `json:"role"`
	Model string `json:"model,omitempty"`
}

// BuildAgentFlags constructs the CLI flags for agent teams. When teams are
// enabled, it generates an --agents flag with a JSON array of agent definitions.
// If no agents are explicitly configured, default agents are generated based
// on the task type.
//
// Deprecated: use SubAgentFlag instead, which produces the correct --agents
// format (object map, not array).
func BuildAgentFlags(cfg TeamsConfig, taskType string) ([]string, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	agents := cfg.Agents
	if len(agents) == 0 {
		agents = defaultAgentsForTaskType(taskType)
	}
	if len(agents) == 0 {
		return nil, nil
	}

	// Sort agent names for deterministic output.
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)

	// Respect MaxTeammates limit.
	limit := len(names)
	if cfg.MaxTeammates > 0 && cfg.MaxTeammates < limit {
		limit = cfg.MaxTeammates
	}

	entries := make([]agentFlagEntry, 0, limit)
	for _, name := range names[:limit] {
		def := agents[name]
		entries = append(entries, agentFlagEntry{
			Name:  name,
			Role:  def.Role,
			Model: def.Model,
		})
	}

	data, err := json.Marshal(entries)
	if err != nil {
		return nil, fmt.Errorf("marshalling agent definitions: %w", err)
	}

	return []string{"--agents", string(data)}, nil
}

// TeamsEnvVars returns the environment variables required to enable agent
// teams mode inside the execution container.
//
// Deprecated: use SubAgentEnvVars instead.
func TeamsEnvVars(cfg TeamsConfig) map[string]string {
	if !cfg.Enabled {
		return nil
	}

	env := map[string]string{
		"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS": "1",
	}

	if cfg.MaxTeammates > 0 {
		env["CLAUDE_CODE_MAX_TEAMMATES"] = strconv.Itoa(cfg.MaxTeammates)
	}

	return env
}

// defaultAgentsForTaskType returns the default agent team for a given task
// type. Returns nil for unrecognised task types.
func defaultAgentsForTaskType(taskType string) map[string]AgentDef {
	switch taskType {
	case "bug_fix":
		return map[string]AgentDef{
			"coder":    {Role: "Write code to fix the issue", Model: "opus"},
			"reviewer": {Role: "Review code changes for correctness", Model: "haiku"},
		}
	case "feature":
		return map[string]AgentDef{
			"coder":    {Role: "Write code to implement the feature", Model: "opus"},
			"reviewer": {Role: "Review code changes for correctness", Model: "haiku"},
			"tester":   {Role: "Write and run tests for the new feature", Model: "sonnet"},
		}
	default:
		return nil
	}
}
