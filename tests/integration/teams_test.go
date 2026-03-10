//go:build integration

// Package integration_test contains integration tests that verify the
// multi-agent teams feature for Claude Code, including team environment
// variables, --teammate-mode flag, and prompt section rendering.
package integration_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/promptbuilder"
	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/engine/claudecode"
)

// teamsTestTask returns a standard task for teams integration tests.
func teamsTestTask(taskType string) engine.Task {
	return engine.Task{
		ID:          "teams-test-1",
		TicketID:    "TICKET-TEAMS-1",
		Title:       "Teams test task",
		Description: "Test description for teams",
		RepoURL:     "https://github.com/org/repo",
		Metadata:    map[string]string{"task_type": taskType},
	}
}

// TestTeamsTeammateModeInSpec verifies that when teams are enabled, the
// resulting ExecutionSpec command includes a --teammate-mode flag.
func TestTeamsTeammateModeInSpec(t *testing.T) {
	t.Parallel()

	eng := claudecode.New(claudecode.WithTeamsConfig(claudecode.TeamsConfig{
		Enabled:      true,
		Mode:         "in-process",
		MaxTeammates: 3,
	}))

	spec, err := eng.BuildExecutionSpec(teamsTestTask("bug_fix"), engine.EngineConfig{})
	require.NoError(t, err)
	require.NotNil(t, spec)

	// Find --teammate-mode in the command.
	modeIdx := -1
	for i, arg := range spec.Command {
		if arg == "--teammate-mode" {
			modeIdx = i
			break
		}
	}
	require.Greater(t, modeIdx, -1, "command must contain --teammate-mode flag")
	require.Greater(t, len(spec.Command), modeIdx+1, "--teammate-mode must have a value")
	assert.Equal(t, "in-process", spec.Command[modeIdx+1])
}

// TestTeamsNoAgentsFlagInSpec verifies that agent teams do NOT produce an
// --agents flag (that is a sub-agents feature, not an agent teams feature).
func TestTeamsNoAgentsFlagInSpec(t *testing.T) {
	t.Parallel()

	eng := claudecode.New(claudecode.WithTeamsConfig(claudecode.TeamsConfig{
		Enabled:      true,
		Mode:         "in-process",
		MaxTeammates: 3,
	}))

	spec, err := eng.BuildExecutionSpec(teamsTestTask("bug_fix"), engine.EngineConfig{})
	require.NoError(t, err)
	require.NotNil(t, spec)

	for _, arg := range spec.Command {
		assert.NotEqual(t, "--agents", arg,
			"agent teams must not produce --agents flag (that is for sub-agents)")
	}
}

// TestTeamsEnvironmentVariables verifies that when teams are enabled, the
// correct environment variables are set in the ExecutionSpec.
func TestTeamsEnvironmentVariables(t *testing.T) {
	t.Parallel()

	eng := claudecode.New(claudecode.WithTeamsConfig(claudecode.TeamsConfig{
		Enabled:      true,
		Mode:         "in-process",
		MaxTeammates: 4,
	}))

	spec, err := eng.BuildExecutionSpec(teamsTestTask("bug_fix"), engine.EngineConfig{})
	require.NoError(t, err)
	require.NotNil(t, spec)

	assert.Equal(t, "1", spec.Env["CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"],
		"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS must be set to 1")
	assert.Equal(t, "4", spec.Env["CLAUDE_CODE_MAX_TEAMMATES"],
		"CLAUDE_CODE_MAX_TEAMMATES must match configured MaxTeammates")
}

// TestTeamsDisabledProducesNoFlags verifies that when teams are not enabled,
// TeamsFlags returns nil.
func TestTeamsDisabledProducesNoFlags(t *testing.T) {
	t.Parallel()

	flags := claudecode.TeamsFlags(claudecode.TeamsConfig{
		Enabled: false,
	})
	assert.Nil(t, flags, "disabled teams should produce no flags")
}

// TestTeamsEnvVarsDisabledReturnsNil verifies that TeamsEnvVars returns nil
// when teams are disabled.
func TestTeamsEnvVarsDisabledReturnsNil(t *testing.T) {
	t.Parallel()

	env := claudecode.TeamsEnvVars(claudecode.TeamsConfig{Enabled: false})
	assert.Nil(t, env, "disabled teams should return no environment variables")
}

// TestTeamsPromptSection verifies that building a prompt with team agents
// includes a "Team Coordination" section listing the agents and their roles.
func TestTeamsPromptSection(t *testing.T) {
	t.Parallel()

	pb, err := promptbuilder.New()
	require.NoError(t, err)

	task := engine.Task{
		ID:          "prompt-teams-1",
		Title:       "Feature with teams",
		Description: "Implement a feature using teams",
		RepoURL:     "https://github.com/org/repo",
	}

	agents := []promptbuilder.TeamAgent{
		{Name: "coder", Role: "Write code to implement the feature"},
		{Name: "reviewer", Role: "Review code changes"},
	}

	prompt, err := pb.BuildPromptWithTeams(task, "", "claude-code", "", nil, agents)
	require.NoError(t, err)

	assert.Contains(t, prompt, "Team Coordination",
		"prompt must contain Team Coordination section")
	assert.Contains(t, prompt, "coder", "prompt must list coder agent")
	assert.Contains(t, prompt, "reviewer", "prompt must list reviewer agent")
	assert.Contains(t, prompt, "Write code to implement the feature",
		"prompt must include agent role descriptions")
}

// TestTeamsPromptSectionEmpty verifies that an empty agent list produces
// no team coordination content.
func TestTeamsPromptSectionEmpty(t *testing.T) {
	t.Parallel()

	section := promptbuilder.TeamCoordinationSection(nil)
	assert.Empty(t, section, "nil agents should produce empty coordination section")

	section = promptbuilder.TeamCoordinationSection([]promptbuilder.TeamAgent{})
	assert.Empty(t, section, "empty agents should produce empty coordination section")
}

// TestTeamsDefaultModeInProcess verifies that empty mode defaults to in-process.
func TestTeamsDefaultModeInProcess(t *testing.T) {
	t.Parallel()

	flags := claudecode.TeamsFlags(claudecode.TeamsConfig{
		Enabled: true,
		// Mode deliberately omitted — should default to "in-process".
	})
	assert.Equal(t, []string{"--teammate-mode", "in-process"}, flags)
}
