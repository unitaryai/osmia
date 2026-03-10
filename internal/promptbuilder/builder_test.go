package promptbuilder

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/pkg/engine"
)

func sampleTask() engine.Task {
	return engine.Task{
		ID:          "task-123",
		TicketID:    "GH-456",
		Title:       "Fix authentication bug",
		Description: "The login endpoint returns 500 when the password contains special characters.",
		RepoURL:     "https://github.com/example/repo",
	}
}

func TestNew(t *testing.T) {
	pb, err := New()
	require.NoError(t, err)
	require.NotNil(t, pb)
}

func TestBuildPrompt(t *testing.T) {
	tests := []struct {
		name       string
		task       engine.Task
		guardRails string
		engineName string
		wantParts  []string
		noParts    []string
	}{
		{
			name:       "basic prompt with all fields",
			task:       sampleTask(),
			guardRails: "Do not modify CI configuration files.",
			engineName: "claude-code",
			wantParts: []string{
				"**ID:** task-123",
				"**Title:** Fix authentication bug",
				"**Repository:** https://github.com/example/repo",
				"The login endpoint returns 500",
				"## Guard Rails",
				"Do not modify CI configuration files.",
				"Running on engine: claude-code",
			},
		},
		{
			name:       "prompt without guard rails",
			task:       sampleTask(),
			guardRails: "",
			engineName: "codex",
			wantParts: []string{
				"**ID:** task-123",
				"Running on engine: codex",
			},
			noParts: []string{
				"## Guard Rails",
			},
		},
		{
			name:       "prompt without engine name",
			task:       sampleTask(),
			guardRails: "Some rules.",
			engineName: "",
			wantParts: []string{
				"**ID:** task-123",
				"## Guard Rails",
				"Some rules.",
			},
			noParts: []string{
				"## Engine",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pb, err := New()
			require.NoError(t, err)

			result, err := pb.BuildPrompt(tt.task, tt.guardRails, tt.engineName)
			require.NoError(t, err)

			for _, part := range tt.wantParts {
				assert.Contains(t, result, part)
			}
			for _, part := range tt.noParts {
				assert.NotContains(t, result, part)
			}
		})
	}
}

func TestBuildPromptWithProfile(t *testing.T) {
	profiles := map[string]TaskProfile{
		"bug-fix": {
			AllowedFilePatterns:   []string{"*.go", "*.py"},
			BlockedFilePatterns:   []string{".env", "*.key"},
			BlockedCommands:       []string{"rm -rf", "drop table"},
			MaxCostPerJob:         5.0,
			MaxJobDurationMinutes: 30,
		},
	}

	t.Run("matching profile is injected", func(t *testing.T) {
		pb, err := New()
		require.NoError(t, err)

		result, err := pb.BuildPromptWithProfile(
			sampleTask(),
			"Guard rails content",
			"claude-code",
			"bug-fix",
			profiles,
		)
		require.NoError(t, err)

		assert.Contains(t, result, "## Task Profile Constraints")
		assert.Contains(t, result, "*.go")
		assert.Contains(t, result, ".env")
		assert.Contains(t, result, "rm -rf")
		assert.Contains(t, result, "$5.00")
		assert.Contains(t, result, "30 minutes")
	})

	t.Run("unknown task type omits profile section", func(t *testing.T) {
		pb, err := New()
		require.NoError(t, err)

		result, err := pb.BuildPromptWithProfile(
			sampleTask(),
			"",
			"claude-code",
			"unknown-type",
			profiles,
		)
		require.NoError(t, err)

		assert.NotContains(t, result, "## Task Profile Constraints")
	})

	t.Run("nil profiles map omits profile section", func(t *testing.T) {
		pb, err := New()
		require.NoError(t, err)

		result, err := pb.BuildPromptWithProfile(
			sampleTask(),
			"",
			"claude-code",
			"bug-fix",
			nil,
		)
		require.NoError(t, err)

		assert.NotContains(t, result, "## Task Profile Constraints")
	})
}

func TestLoadGuardRails(t *testing.T) {
	t.Run("loads existing file", func(t *testing.T) {
		content := "# Guard Rails\n\n- Do not delete production data\n- Always run tests"
		tmp := filepath.Join(t.TempDir(), "guardrails.md")
		err := os.WriteFile(tmp, []byte(content), 0o600)
		require.NoError(t, err)

		loaded, err := LoadGuardRails(tmp)
		require.NoError(t, err)
		assert.Equal(t, content, loaded)
	})

	t.Run("returns error for missing file", func(t *testing.T) {
		_, err := LoadGuardRails("/nonexistent/guardrails.md")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading guard rails file")
	})
}

func TestBuildPrompt_TemplateSafety(t *testing.T) {
	// Verify that adversarial content in task descriptions does not break
	// the template or enable injection. The template uses structured fields,
	// not raw string interpolation.
	task := engine.Task{
		ID:          "task-inject",
		Title:       "{{.MaliciousField}}",
		Description: "{{printf \"%s\" .GuardRails}}",
		RepoURL:     "https://github.com/example/repo",
	}

	pb, err := New()
	require.NoError(t, err)

	result, err := pb.BuildPrompt(task, "secret guard rails", "claude-code")
	require.NoError(t, err)

	// The template markers should appear literally, not be interpreted.
	assert.Contains(t, result, "{{.MaliciousField}}")
	assert.Contains(t, result, "{{printf")
	// Guard rails content should appear in the guard rails section, not injected
	// into the description.
	assert.Contains(t, result, "secret guard rails")
}

func TestWorkflowInstructions(t *testing.T) {
	tests := []struct {
		name     string
		workflow string
		wantPart string
		wantNone bool
	}{
		{
			name:     "empty workflow returns empty string",
			workflow: "",
			wantNone: true,
		},
		{
			name:     "tdd workflow returns TDD instructions",
			workflow: "tdd",
			wantPart: "## Workflow: Test-Driven Development",
		},
		{
			name:     "tdd workflow includes run existing tests step",
			workflow: "tdd",
			wantPart: "Run the existing test suite",
		},
		{
			name:     "tdd workflow includes write failing test step",
			workflow: "tdd",
			wantPart: "Write a failing test",
		},
		{
			name:     "review-first workflow returns review instructions",
			workflow: "review-first",
			wantPart: "## Workflow: Review First",
		},
		{
			name:     "review-first workflow includes read code step",
			workflow: "review-first",
			wantPart: "Read and understand all relevant code",
		},
		{
			name:     "unknown workflow returns empty string",
			workflow: "unknown-mode",
			wantNone: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := WorkflowInstructions(tt.workflow)
			if tt.wantNone {
				assert.Empty(t, result)
			} else {
				assert.Contains(t, result, tt.wantPart)
			}
		})
	}
}

func TestBuildPromptWithProfile_Workflow(t *testing.T) {
	tests := []struct {
		name      string
		profiles  map[string]TaskProfile
		taskType  string
		wantParts []string
		noParts   []string
	}{
		{
			name: "tdd workflow instructions are included",
			profiles: map[string]TaskProfile{
				"bug-fix": {
					Workflow:              "tdd",
					AllowedFilePatterns:   []string{"*.go"},
					BlockedFilePatterns:   []string{".env"},
					BlockedCommands:       []string{"rm -rf"},
					MaxCostPerJob:         5.0,
					MaxJobDurationMinutes: 30,
				},
			},
			taskType: "bug-fix",
			wantParts: []string{
				"## Workflow: Test-Driven Development",
				"Write a failing test",
				"## Task Profile Constraints",
			},
		},
		{
			name: "review-first workflow instructions are included",
			profiles: map[string]TaskProfile{
				"review": {
					Workflow:              "review-first",
					AllowedFilePatterns:   []string{"*.go"},
					BlockedFilePatterns:   []string{".env"},
					BlockedCommands:       []string{"rm -rf"},
					MaxCostPerJob:         5.0,
					MaxJobDurationMinutes: 30,
				},
			},
			taskType: "review",
			wantParts: []string{
				"## Workflow: Review First",
				"Read and understand all relevant code",
			},
		},
		{
			name: "empty workflow omits workflow section",
			profiles: map[string]TaskProfile{
				"feature": {
					Workflow:              "",
					AllowedFilePatterns:   []string{"*.go"},
					BlockedFilePatterns:   []string{".env"},
					BlockedCommands:       []string{"rm -rf"},
					MaxCostPerJob:         5.0,
					MaxJobDurationMinutes: 30,
				},
			},
			taskType: "feature",
			noParts: []string{
				"## Workflow:",
			},
		},
		{
			name: "unknown task type omits workflow section",
			profiles: map[string]TaskProfile{
				"bug-fix": {
					Workflow:              "tdd",
					AllowedFilePatterns:   []string{"*.go"},
					BlockedFilePatterns:   []string{".env"},
					BlockedCommands:       []string{"rm -rf"},
					MaxCostPerJob:         5.0,
					MaxJobDurationMinutes: 30,
				},
			},
			taskType: "unknown-type",
			noParts: []string{
				"## Workflow:",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pb, err := New()
			require.NoError(t, err)

			result, err := pb.BuildPromptWithProfile(
				sampleTask(),
				"Guard rails content",
				"claude-code",
				tt.taskType,
				tt.profiles,
			)
			require.NoError(t, err)

			for _, part := range tt.wantParts {
				assert.Contains(t, result, part)
			}
			for _, part := range tt.noParts {
				assert.NotContains(t, result, part)
			}
		})
	}
}

func TestBuildPromptWithProfile_WorkflowOrdering(t *testing.T) {
	// Verify that workflow instructions appear after the task description
	// but before guard rails and task profile constraints.
	profiles := map[string]TaskProfile{
		"bug-fix": {
			Workflow:              "tdd",
			AllowedFilePatterns:   []string{"*.go"},
			BlockedFilePatterns:   []string{".env"},
			BlockedCommands:       []string{"rm -rf"},
			MaxCostPerJob:         5.0,
			MaxJobDurationMinutes: 30,
		},
	}

	pb, err := New()
	require.NoError(t, err)

	result, err := pb.BuildPromptWithProfile(
		sampleTask(),
		"Do not modify CI configuration.",
		"claude-code",
		"bug-fix",
		profiles,
	)
	require.NoError(t, err)

	descIdx := indexOf(result, "## Description")
	workflowIdx := indexOf(result, "## Workflow: Test-Driven Development")
	guardRailsIdx := indexOf(result, "## Guard Rails")
	profileIdx := indexOf(result, "## Task Profile Constraints")

	require.NotEqual(t, -1, descIdx, "description section not found")
	require.NotEqual(t, -1, workflowIdx, "workflow section not found")
	require.NotEqual(t, -1, guardRailsIdx, "guard rails section not found")
	require.NotEqual(t, -1, profileIdx, "task profile section not found")

	assert.Less(t, descIdx, workflowIdx, "workflow should appear after description")
	assert.Less(t, workflowIdx, guardRailsIdx, "workflow should appear before guard rails")
	assert.Less(t, guardRailsIdx, profileIdx, "guard rails should appear before task profile")
}

// indexOf returns the index of the first occurrence of substr in s,
// or -1 if not found.
func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func TestBuildPrompt_EmptyTask(t *testing.T) {
	pb, err := New()
	require.NoError(t, err)

	result, err := pb.BuildPrompt(engine.Task{}, "", "")
	require.NoError(t, err)

	// Should still produce valid output with empty fields.
	assert.Contains(t, result, "# Task")
	assert.Contains(t, result, "**ID:**")
}

func TestTeamCoordinationSection(t *testing.T) {
	tests := []struct {
		name      string
		agents    []TeamAgent
		wantParts []string
		wantEmpty bool
	}{
		{
			name:      "nil agents returns empty string",
			agents:    nil,
			wantEmpty: true,
		},
		{
			name:      "empty agents returns empty string",
			agents:    []TeamAgent{},
			wantEmpty: true,
		},
		{
			name: "single agent included",
			agents: []TeamAgent{
				{Name: "coder", Role: "Write code to fix the issue"},
			},
			wantParts: []string{
				"specialised agents",
				"- **coder**: Write code to fix the issue",
			},
		},
		{
			name: "multiple agents sorted alphabetically",
			agents: []TeamAgent{
				{Name: "reviewer", Role: "Review code changes"},
				{Name: "coder", Role: "Write code"},
				{Name: "tester", Role: "Run tests"},
			},
			wantParts: []string{
				"- **coder**: Write code",
				"- **reviewer**: Review code changes",
				"- **tester**: Run tests",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TeamCoordinationSection(tt.agents)

			if tt.wantEmpty {
				assert.Empty(t, result)
				return
			}

			for _, part := range tt.wantParts {
				assert.Contains(t, result, part)
			}
		})
	}
}

func TestBuildPromptWithTeams(t *testing.T) {
	profiles := map[string]TaskProfile{
		"bug-fix": {
			AllowedFilePatterns:   []string{"*.go"},
			BlockedFilePatterns:   []string{".env"},
			BlockedCommands:       []string{"rm -rf"},
			MaxCostPerJob:         5.0,
			MaxJobDurationMinutes: 30,
		},
	}

	t.Run("teams section included when agents provided", func(t *testing.T) {
		pb, err := New()
		require.NoError(t, err)

		agents := []TeamAgent{
			{Name: "coder", Role: "Write code"},
			{Name: "reviewer", Role: "Review code"},
		}

		result, err := pb.BuildPromptWithTeams(
			sampleTask(),
			"Guard rails content",
			"claude-code",
			"bug-fix",
			profiles,
			agents,
		)
		require.NoError(t, err)

		assert.Contains(t, result, "## Team Coordination")
		assert.Contains(t, result, "- **coder**: Write code")
		assert.Contains(t, result, "- **reviewer**: Review code")
		// Profile and other sections should still be present.
		assert.Contains(t, result, "## Task Profile Constraints")
		assert.Contains(t, result, "Running on engine: claude-code")
	})

	t.Run("teams section omitted when no agents", func(t *testing.T) {
		pb, err := New()
		require.NoError(t, err)

		result, err := pb.BuildPromptWithTeams(
			sampleTask(),
			"Guard rails content",
			"claude-code",
			"bug-fix",
			profiles,
			nil,
		)
		require.NoError(t, err)

		assert.NotContains(t, result, "## Team Coordination")
		// Other sections should still work.
		assert.Contains(t, result, "## Task Profile Constraints")
	})

	t.Run("team coordination appears before engine section", func(t *testing.T) {
		pb, err := New()
		require.NoError(t, err)

		agents := []TeamAgent{
			{Name: "coder", Role: "Write code"},
		}

		result, err := pb.BuildPromptWithTeams(
			sampleTask(),
			"Guard rails content",
			"claude-code",
			"bug-fix",
			profiles,
			agents,
		)
		require.NoError(t, err)

		teamIdx := indexOf(result, "## Team Coordination")
		engineIdx := indexOf(result, "## Engine")

		require.NotEqual(t, -1, teamIdx, "team coordination section not found")
		require.NotEqual(t, -1, engineIdx, "engine section not found")
		assert.Less(t, teamIdx, engineIdx, "team coordination should appear before engine")
	})
}
