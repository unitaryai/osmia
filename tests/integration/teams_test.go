//go:build integration

// Package integration_test contains integration tests that verify the
// multi-agent teams feature for Claude Code, including agent flag generation,
// default agent selection by task type, team environment variables, and
// prompt section rendering.
package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/robodev/internal/promptbuilder"
	"github.com/unitaryai/robodev/pkg/engine"
	"github.com/unitaryai/robodev/pkg/engine/claudecode"
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

// TestTeamsAgentFlagsInSpec verifies that when teams are enabled, the
// resulting ExecutionSpec command includes an --agents flag with valid JSON.
func TestTeamsAgentFlagsInSpec(t *testing.T) {
	t.Parallel()

	eng := claudecode.New(claudecode.WithTeamsConfig(claudecode.TeamsConfig{
		Enabled:      true,
		MaxTeammates: 3,
		Agents: map[string]claudecode.AgentDef{
			"coder": {Role: "Write code", Model: "opus"},
		},
	}))

	spec, err := eng.BuildExecutionSpec(teamsTestTask("bug_fix"), engine.EngineConfig{})
	require.NoError(t, err)
	require.NotNil(t, spec)

	// Find --agents in the command.
	agentsIdx := -1
	for i, arg := range spec.Command {
		if arg == "--agents" {
			agentsIdx = i
			break
		}
	}
	require.Greater(t, agentsIdx, -1, "command must contain --agents flag")
	require.Greater(t, len(spec.Command), agentsIdx+1, "--agents must have a value")

	// Verify the value is valid JSON.
	var agents []map[string]interface{}
	err = json.Unmarshal([]byte(spec.Command[agentsIdx+1]), &agents)
	require.NoError(t, err, "agent flag value must be valid JSON")
	assert.Len(t, agents, 1)
	assert.Equal(t, "coder", agents[0]["name"])
}

// TestTeamsDefaultAgentsBugFix verifies that for "bug_fix" task type with
// no custom agents, the default agents include coder and reviewer.
func TestTeamsDefaultAgentsBugFix(t *testing.T) {
	t.Parallel()

	flags, err := claudecode.BuildAgentFlags(claudecode.TeamsConfig{
		Enabled:      true,
		MaxTeammates: 5,
	}, "bug_fix")
	require.NoError(t, err)
	require.Len(t, flags, 2, "should return --agents and its value")

	assert.Equal(t, "--agents", flags[0])

	var agents []map[string]interface{}
	err = json.Unmarshal([]byte(flags[1]), &agents)
	require.NoError(t, err)

	names := make([]string, 0, len(agents))
	for _, a := range agents {
		names = append(names, a["name"].(string))
	}
	assert.Contains(t, names, "coder", "bug_fix defaults must include coder")
	assert.Contains(t, names, "reviewer", "bug_fix defaults must include reviewer")
	assert.Len(t, names, 2, "bug_fix should have exactly 2 agents")
}

// TestTeamsDefaultAgentsFeature verifies that for "feature" task type with
// no custom agents, the default agents include coder, reviewer, and tester.
func TestTeamsDefaultAgentsFeature(t *testing.T) {
	t.Parallel()

	flags, err := claudecode.BuildAgentFlags(claudecode.TeamsConfig{
		Enabled:      true,
		MaxTeammates: 5,
	}, "feature")
	require.NoError(t, err)
	require.Len(t, flags, 2)

	var agents []map[string]interface{}
	err = json.Unmarshal([]byte(flags[1]), &agents)
	require.NoError(t, err)

	names := make([]string, 0, len(agents))
	for _, a := range agents {
		names = append(names, a["name"].(string))
	}
	assert.Contains(t, names, "coder", "feature defaults must include coder")
	assert.Contains(t, names, "reviewer", "feature defaults must include reviewer")
	assert.Contains(t, names, "tester", "feature defaults must include tester")
	assert.Len(t, names, 3, "feature should have exactly 3 agents")
}

// TestTeamsCustomAgentsFromConfig verifies that custom agent definitions
// from the config are reflected in the generated flags.
func TestTeamsCustomAgentsFromConfig(t *testing.T) {
	t.Parallel()

	customAgents := map[string]claudecode.AgentDef{
		"architect": {Role: "Design the system", Model: "opus"},
		"deployer":  {Role: "Deploy the changes", Model: "sonnet"},
	}

	flags, err := claudecode.BuildAgentFlags(claudecode.TeamsConfig{
		Enabled:      true,
		MaxTeammates: 5,
		Agents:       customAgents,
	}, "bug_fix") // task type is ignored when custom agents are provided
	require.NoError(t, err)
	require.Len(t, flags, 2)

	var agents []map[string]interface{}
	err = json.Unmarshal([]byte(flags[1]), &agents)
	require.NoError(t, err)
	assert.Len(t, agents, 2)

	names := make([]string, 0, len(agents))
	for _, a := range agents {
		names = append(names, a["name"].(string))
	}
	assert.Contains(t, names, "architect")
	assert.Contains(t, names, "deployer")
}

// TestTeamsEnvironmentVariables verifies that when teams are enabled, the
// correct environment variables are set in the ExecutionSpec.
func TestTeamsEnvironmentVariables(t *testing.T) {
	t.Parallel()

	eng := claudecode.New(claudecode.WithTeamsConfig(claudecode.TeamsConfig{
		Enabled:      true,
		MaxTeammates: 4,
		Agents: map[string]claudecode.AgentDef{
			"coder": {Role: "Write code"},
		},
	}))

	spec, err := eng.BuildExecutionSpec(teamsTestTask("bug_fix"), engine.EngineConfig{})
	require.NoError(t, err)
	require.NotNil(t, spec)

	assert.Equal(t, "1", spec.Env["CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"],
		"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS must be set to 1")
	assert.Equal(t, "4", spec.Env["CLAUDE_CODE_MAX_TEAMMATES"],
		"CLAUDE_CODE_MAX_TEAMMATES must match configured MaxTeammates")
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

// TestTeamsDisabledProducesNoFlags verifies that when teams are not enabled,
// BuildAgentFlags returns nil.
func TestTeamsDisabledProducesNoFlags(t *testing.T) {
	t.Parallel()

	flags, err := claudecode.BuildAgentFlags(claudecode.TeamsConfig{
		Enabled: false,
	}, "bug_fix")
	require.NoError(t, err)
	assert.Nil(t, flags, "disabled teams should produce no flags")
}

// TestTeamsEnvVarsDisabledReturnsNil verifies that TeamsEnvVars returns nil
// when teams are disabled.
func TestTeamsEnvVarsDisabledReturnsNil(t *testing.T) {
	t.Parallel()

	env := claudecode.TeamsEnvVars(claudecode.TeamsConfig{Enabled: false})
	assert.Nil(t, env, "disabled teams should return no environment variables")
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

// TestTeamsAgentFlagsSorted verifies that agent names in the --agents flag
// are sorted alphabetically for deterministic output.
func TestTeamsAgentFlagsSorted(t *testing.T) {
	t.Parallel()

	flags, err := claudecode.BuildAgentFlags(claudecode.TeamsConfig{
		Enabled:      true,
		MaxTeammates: 5,
		Agents: map[string]claudecode.AgentDef{
			"zebra":    {Role: "Last"},
			"alpha":    {Role: "First"},
			"mid":      {Role: "Middle"},
		},
	}, "")
	require.NoError(t, err)
	require.Len(t, flags, 2)

	var agents []map[string]interface{}
	err = json.Unmarshal([]byte(flags[1]), &agents)
	require.NoError(t, err)
	require.Len(t, agents, 3)

	// Verify alphabetical order.
	names := make([]string, 0, len(agents))
	for _, a := range agents {
		names = append(names, a["name"].(string))
	}
	assert.Equal(t, []string{"alpha", "mid", "zebra"}, names,
		"agents must be sorted alphabetically")

	// Also verify the raw JSON contains them in order.
	alphaIdx := strings.Index(flags[1], "alpha")
	midIdx := strings.Index(flags[1], "mid")
	zebraIdx := strings.Index(flags[1], "zebra")
	assert.Less(t, alphaIdx, midIdx)
	assert.Less(t, midIdx, zebraIdx)
}
