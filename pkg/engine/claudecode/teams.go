// Package claudecode provides the Claude Code execution engine for Osmia.
// This file implements the agent teams configuration for Claude Code.
// Agent teams are a distinct feature from sub-agents: they spawn multiple
// independent Claude Code instances with shared task lists and inter-agent
// messaging, coordinated by a team lead. Claude dynamically creates the
// team based on the task — agents are not pre-defined by the user.
package claudecode

import (
	"strconv"
)

// TeamsConfig configures the experimental agent teams feature for Claude Code.
// Agent teams spawn multiple independent Claude Code instances that collaborate
// via shared task lists and inter-agent messaging. The team lead coordinates
// work distribution dynamically based on the task.
//
// This is distinct from sub-agents (see subagents.go), which are lightweight
// helpers within a single Claude Code session.
type TeamsConfig struct {
	// Enabled controls whether agent teams mode is active. Defaults to false.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Mode specifies the teams execution mode. Use "in-process" for headless
	// containers (no tmux required). Defaults to "in-process".
	Mode string `json:"mode" yaml:"mode"`

	// MaxTeammates is the maximum number of teammate agents that can be
	// spawned within a single job. Defaults to 3.
	MaxTeammates int `json:"max_teammates" yaml:"max_teammates"`
}

// DefaultTeamsConfig returns the default agent teams configuration.
func DefaultTeamsConfig() TeamsConfig {
	return TeamsConfig{
		Enabled:      false,
		Mode:         "in-process",
		MaxTeammates: 3,
	}
}

// TeamsEnvVars returns the environment variables required to enable agent
// teams mode inside the execution container.
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

// TeamsFlags returns the CLI flags for agent teams. When teams are enabled
// and a mode is configured, it produces the --teammate-mode flag. Agent teams
// do not use --agents (that is for sub-agents); the team lead dynamically
// creates teammates based on the task.
func TeamsFlags(cfg TeamsConfig) []string {
	if !cfg.Enabled {
		return nil
	}

	mode := cfg.Mode
	if mode == "" {
		mode = "in-process"
	}

	return []string{"--teammate-mode", mode}
}
