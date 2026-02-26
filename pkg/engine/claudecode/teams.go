// Package claudecode provides the Claude Code execution engine for RoboDev.
// This file implements experimental agent teams configuration for Claude Code.
package claudecode

// TeamsConfig configures the experimental agent teams feature for Claude Code.
// Agent teams allow splitting a task across multiple Claude Code instances
// running in-process within a single K8s Job pod.
type TeamsConfig struct {
	// Enabled controls whether agent teams mode is active. Defaults to false.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Mode specifies the teams execution mode. Currently only "in-process"
	// is supported (no tmux required inside containers).
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

	return map[string]string{
		"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS": "1",
	}
}
