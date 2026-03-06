package claudecode

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubAgentFlag_Empty(t *testing.T) {
	flags, err := SubAgentFlag(nil)
	require.NoError(t, err)
	assert.Nil(t, flags)

	flags, err = SubAgentFlag([]SubAgent{})
	require.NoError(t, err)
	assert.Nil(t, flags)
}

func TestSubAgentFlag_SingleInline(t *testing.T) {
	agents := []SubAgent{
		{
			Name:        "reviewer",
			Description: "Reviews code changes for correctness",
			Prompt:      "You are a code reviewer.",
			Model:       "haiku",
		},
	}

	flags, err := SubAgentFlag(agents)
	require.NoError(t, err)
	require.Len(t, flags, 2)
	assert.Equal(t, "--agents", flags[0])

	// Parse the JSON to verify structure.
	var m map[string]map[string]any
	require.NoError(t, json.Unmarshal([]byte(flags[1]), &m))

	reviewer, ok := m["reviewer"]
	require.True(t, ok, "expected 'reviewer' key in agents map")
	assert.Equal(t, "Reviews code changes for correctness", reviewer["description"])
	assert.Equal(t, "You are a code reviewer.", reviewer["prompt"])
	assert.Equal(t, "haiku", reviewer["model"])
}

func TestSubAgentFlag_MultipleWithAllFields(t *testing.T) {
	agents := []SubAgent{
		{
			Name:            "coder",
			Description:     "Writes code",
			Prompt:          "You write code.",
			Model:           "opus",
			Tools:           []string{"Read", "Write", "Bash"},
			DisallowedTools: []string{"WebFetch"},
			PermissionMode:  "bypassPermissions",
			MaxTurns:        20,
			Skills:          []string{"tdd"},
			Background:      true,
		},
		{
			Name:        "tester",
			Description: "Runs tests",
		},
	}

	flags, err := SubAgentFlag(agents)
	require.NoError(t, err)
	require.Len(t, flags, 2)

	var m map[string]map[string]any
	require.NoError(t, json.Unmarshal([]byte(flags[1]), &m))

	coder := m["coder"]
	assert.Equal(t, "Writes code", coder["description"])
	assert.Equal(t, "You write code.", coder["prompt"])
	assert.Equal(t, "opus", coder["model"])
	assert.Equal(t, "bypassPermissions", coder["permissionMode"])
	assert.Equal(t, float64(20), coder["maxTurns"])
	assert.Equal(t, true, coder["background"])
	assert.Len(t, coder["tools"], 3)
	assert.Len(t, coder["disallowedTools"], 1)
	assert.Len(t, coder["skills"], 1)

	tester := m["tester"]
	assert.Equal(t, "Runs tests", tester["description"])
	// Optional fields should be absent when unset.
	assert.NotContains(t, tester, "prompt")
	assert.NotContains(t, tester, "model")
}

func TestSubAgentFlag_ConfigMapAgentsExcluded(t *testing.T) {
	agents := []SubAgent{
		{Name: "inline-agent", Description: "Inline"},
		{Name: "cm-agent", Description: "From ConfigMap", ConfigMap: "my-cm"},
	}

	flags, err := SubAgentFlag(agents)
	require.NoError(t, err)
	require.Len(t, flags, 2)

	var m map[string]map[string]any
	require.NoError(t, json.Unmarshal([]byte(flags[1]), &m))

	assert.Contains(t, m, "inline-agent")
	assert.NotContains(t, m, "cm-agent")
}

func TestSubAgentFlag_AllConfigMapReturnsNil(t *testing.T) {
	agents := []SubAgent{
		{Name: "cm-only", Description: "From CM", ConfigMap: "cm"},
	}

	flags, err := SubAgentFlag(agents)
	require.NoError(t, err)
	assert.Nil(t, flags)
}

func TestSubAgentEnvVars_ConfigMap(t *testing.T) {
	agents := []SubAgent{
		{Name: "reviewer", ConfigMap: "reviewer-cm"},
		{Name: "tester", ConfigMap: "tester-cm"},
	}

	env := SubAgentEnvVars(agents)
	require.NotNil(t, env)
	assert.Equal(t, "/subagents/reviewer.md", env["CLAUDE_SUBAGENT_PATH_REVIEWER"])
	assert.Equal(t, "/subagents/tester.md", env["CLAUDE_SUBAGENT_PATH_TESTER"])
}

func TestSubAgentEnvVars_InlineOnly(t *testing.T) {
	agents := []SubAgent{
		{Name: "coder", Description: "Writes code"},
	}

	env := SubAgentEnvVars(agents)
	assert.Nil(t, env)
}

func TestSubAgentVolumes_ConfigMap(t *testing.T) {
	agents := []SubAgent{
		{Name: "reviewer", ConfigMap: "reviewer-cm"},
		{Name: "custom", ConfigMap: "custom-cm", Key: "prompt.md"},
	}

	vols := SubAgentVolumes(agents)
	require.Len(t, vols, 2)

	assert.Equal(t, "subagent-reviewer", vols[0].Name)
	assert.Equal(t, "/subagents/reviewer.md", vols[0].MountPath)
	assert.Equal(t, "reviewer.md", vols[0].SubPath)
	assert.Equal(t, "reviewer-cm", vols[0].ConfigMapName)
	assert.Equal(t, "reviewer.md", vols[0].ConfigMapKey)
	assert.True(t, vols[0].ReadOnly)

	assert.Equal(t, "subagent-custom", vols[1].Name)
	assert.Equal(t, "prompt.md", vols[1].SubPath)
	assert.Equal(t, "prompt.md", vols[1].ConfigMapKey)
}

func TestSubAgentVolumes_NoConfigMap(t *testing.T) {
	agents := []SubAgent{
		{Name: "coder", Description: "Writes code"},
	}

	vols := SubAgentVolumes(agents)
	assert.Nil(t, vols)
}
