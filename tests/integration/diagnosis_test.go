//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/diagnosis"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/pkg/engine"
)

func diagIntegrationLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// TestDiagnosis_FullPipeline tests the complete diagnosis pipeline:
// analyse failure -> prescribe corrective instructions -> build retry spec.
func TestDiagnosis_FullPipeline(t *testing.T) {
	ctx := context.Background()
	logger := diagIntegrationLogger()

	analyser := diagnosis.NewAnalyser(logger)
	builder := diagnosis.NewRetryBuilder(logger)

	// Simulate a failed TaskRun with dependency errors in events.
	tr := taskrun.New("tr-diag-1", "idem-diag-1", "ticket-diag-1", "claude-code")
	tr.State = taskrun.StateRunning
	tr.CurrentEngine = "claude-code"

	events := []*agentstream.StreamEvent{
		{Parsed: &agentstream.ContentDeltaEvent{
			Content: "Error: module not found: github.com/missing/package",
		}},
		{Parsed: &agentstream.ToolCallEvent{
			Tool: "Bash",
			Args: json.RawMessage(`{"command": "go build ./..."}`),
		}},
		{Parsed: &agentstream.ContentDeltaEvent{
			Content: "Build failed with import error",
		}},
	}

	input := diagnosis.DiagnosisInput{
		TaskRun: tr,
		Events:  events,
		Result: &engine.TaskResult{
			Success: false,
			Summary: "build failed due to missing dependencies",
		},
	}

	// Step 1: Analyse.
	diag, err := analyser.Analyse(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, diag)
	assert.Equal(t, diagnosis.DependencyMissing, diag.Mode)
	assert.True(t, diag.Confidence > 0.5)
	assert.NotEmpty(t, diag.Evidence)

	// Step 2: Build retry spec.
	originalPrompt := "Fix the login page authentication flow"
	spec, err := builder.Build(ctx, originalPrompt, diag, "claude-code", false)
	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Contains(t, spec.Prompt, originalPrompt)
	assert.Contains(t, spec.Prompt, "dependencies")
	assert.Equal(t, "claude-code", spec.Engine)
	assert.NotEmpty(t, spec.Reason)

	// Step 3: Store in diagnosis history and check dedup.
	record := diagnosis.ToDiagnosisRecord(diag)
	tr.DiagnosisHistory = append(tr.DiagnosisHistory, record)

	shouldRetry := diagnosis.ShouldRetry(tr.DiagnosisHistory, diag, 3)
	assert.False(t, shouldRetry, "same failure mode should not retry")

	// A different failure mode should allow retry.
	differentDiag := &diagnosis.Diagnosis{
		Mode:        diagnosis.WrongApproach,
		Confidence:  0.60,
		Evidence:    []string{"wrong files edited"},
		DiagnosedAt: time.Now(),
	}
	shouldRetryDifferent := diagnosis.ShouldRetry(tr.DiagnosisHistory, differentDiag, 3)
	assert.True(t, shouldRetryDifferent, "different failure mode should allow retry")
}

// TestDiagnosis_PermissionBlockedPipeline tests the full pipeline for
// a permission-blocked failure.
func TestDiagnosis_PermissionBlockedPipeline(t *testing.T) {
	ctx := context.Background()
	logger := diagIntegrationLogger()

	analyser := diagnosis.NewAnalyser(logger)
	builder := diagnosis.NewRetryBuilder(logger)

	tr := taskrun.New("tr-perm", "idem-perm", "ticket-perm", "claude-code")
	tr.State = taskrun.StateRunning

	events := []*agentstream.StreamEvent{
		{Parsed: &agentstream.ContentDeltaEvent{
			Content: "Error: EACCES: permission denied, open '/etc/protected'",
		}},
	}

	input := diagnosis.DiagnosisInput{
		TaskRun: tr,
		Events:  events,
	}

	diag, err := analyser.Analyse(ctx, input)
	require.NoError(t, err)
	assert.Equal(t, diagnosis.PermissionBlocked, diag.Mode)

	spec, err := builder.Build(ctx, "update configuration files", diag, "claude-code", false)
	require.NoError(t, err)
	assert.Contains(t, spec.Prompt, "permissions")
}

// TestDiagnosis_EngineSwitchRecommendation tests that a diagnosis with
// a suggested engine results in the correct engine switch in the retry spec.
func TestDiagnosis_EngineSwitchRecommendation(t *testing.T) {
	ctx := context.Background()
	logger := diagIntegrationLogger()
	builder := diagnosis.NewRetryBuilder(logger)

	diag := &diagnosis.Diagnosis{
		Mode:            diagnosis.ModelConfusion,
		Confidence:      0.80,
		Evidence:        []string{"high oscillation"},
		SuggestedEngine: "codex",
		DiagnosedAt:     time.Now(),
	}

	spec, err := builder.Build(ctx, "implement feature X", diag, "claude-code", true)
	require.NoError(t, err)
	assert.Equal(t, "codex", spec.Engine)
}

// TestDiagnosis_MaxDiagnosesEnforced tests that the maximum number of
// diagnoses per task is enforced.
func TestDiagnosis_MaxDiagnosesEnforced(t *testing.T) {
	history := []taskrun.DiagnosisRecord{
		{Mode: string(diagnosis.WrongApproach)},
		{Mode: string(diagnosis.DependencyMissing)},
		{Mode: string(diagnosis.ScopeCreep)},
	}

	current := &diagnosis.Diagnosis{
		Mode:        diagnosis.TestMisunderstanding,
		Confidence:  0.65,
		DiagnosedAt: time.Now(),
	}

	// With maxDiagnoses=3, should not retry after 3 prior diagnoses.
	assert.False(t, diagnosis.ShouldRetry(history, current, 3))

	// With a higher limit, should allow retry.
	assert.True(t, diagnosis.ShouldRetry(history, current, 5))
}
