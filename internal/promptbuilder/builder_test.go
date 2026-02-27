package promptbuilder

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/robodev/pkg/engine"
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

func TestBuildPrompt_EmptyTask(t *testing.T) {
	pb, err := New()
	require.NoError(t, err)

	result, err := pb.BuildPrompt(engine.Task{}, "", "")
	require.NoError(t, err)

	// Should still produce valid output with empty fields.
	assert.Contains(t, result, "# Task")
	assert.Contains(t, result, "**ID:**")
}
