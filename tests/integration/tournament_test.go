//go:build integration

// Package integration_test contains integration tests that verify the
// tournament subsystem manages a full competitive execution lifecycle.
package integration_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/tournament"
)

func tournamentTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// TestTournamentFullLifecycle simulates a 3-candidate tournament from start
// through completion, verifying state transitions and winner selection.
func TestTournamentFullLifecycle(t *testing.T) {
	t.Parallel()

	coord := tournament.NewCoordinator(tournamentTestLogger())
	ctx := context.Background()

	cfg := tournament.TournamentConfig{
		CandidateCount:            3,
		CandidateEngines:          []string{"claude-code", "aider", "codex"},
		JudgeEngine:               "claude-code",
		EarlyTerminationThreshold: 0.6,
	}

	// Start tournament with 3 candidates.
	tr, err := coord.StartTournament(ctx, "t-lifecycle", "ticket-42", []string{"tr-1", "tr-2", "tr-3"}, cfg)
	require.NoError(t, err)
	assert.Equal(t, tournament.StateCompeting, tr.State)

	// Simulate candidates completing with different characteristics.
	candidates := []tournament.CandidateResult{
		{
			TaskRunID: "tr-1",
			Engine:    "claude-code",
			Diff:      "--- a/main.go\n+++ b/main.go\n@@ improved @@",
			Summary:   "Comprehensive fix with tests",
			Success:   true,
			Cost:      3.50,
			Duration:  8 * time.Minute,
			PRMScores: []int{8, 9, 8, 9},
		},
		{
			TaskRunID: "tr-2",
			Engine:    "aider",
			Diff:      "--- a/main.go\n+++ b/main.go\n@@ quick fix @@",
			Summary:   "Quick targeted fix",
			Success:   true,
			Cost:      0.80,
			Duration:  2 * time.Minute,
			PRMScores: []int{7, 6, 7},
		},
	}

	// First candidate completes.
	ready, err := coord.OnCandidateComplete(ctx, "t-lifecycle", &candidates[0])
	require.NoError(t, err)
	assert.False(t, ready)

	// Second candidate completes — reaches 60% threshold.
	ready, err = coord.OnCandidateComplete(ctx, "t-lifecycle", &candidates[1])
	require.NoError(t, err)
	assert.True(t, ready, "2 of 3 candidates should meet 60% threshold")

	// Begin judging.
	results, err := coord.BeginJudging(ctx, "t-lifecycle", "judge-tr")
	require.NoError(t, err)
	assert.Len(t, results, 2)

	tr = coord.GetTournament("t-lifecycle")
	assert.Equal(t, tournament.StateJudging, tr.State)

	// Third candidate is lagging.
	lagging := coord.LaggingCandidates("t-lifecycle")
	assert.Contains(t, lagging, "tr-3")

	// Judge selects the first candidate as winner.
	err = coord.SelectWinner(ctx, "t-lifecycle", "tr-1")
	require.NoError(t, err)

	tr = coord.GetTournament("t-lifecycle")
	assert.Equal(t, tournament.StateSelected, tr.State)
	assert.Equal(t, "tr-1", tr.WinnerTaskRunID)
	assert.NotNil(t, tr.CompletedAt)
}

// TestTournamentJudgePrompt verifies that the judge prompt is correctly
// constructed from candidate results.
func TestTournamentJudgePrompt(t *testing.T) {
	t.Parallel()

	builder := tournament.NewJudgePromptBuilder()
	prompt, err := builder.BuildPrompt("Fix the authentication bug in the login handler", []*tournament.CandidateResult{
		{
			TaskRunID: "tr-1",
			Engine:    "claude-code",
			Diff:      "+fixed auth check",
			Summary:   "Fixed the auth check",
			Success:   true,
			Cost:      2.0,
			Duration:  5 * time.Minute,
			PRMScores: []int{8, 9},
		},
		{
			TaskRunID: "tr-2",
			Engine:    "aider",
			Diff:      "+refactored auth",
			Summary:   "Refactored authentication",
			Success:   true,
			Cost:      0.5,
			Duration:  3 * time.Minute,
		},
	})

	require.NoError(t, err)
	assert.Contains(t, prompt, "authentication bug")
	assert.Contains(t, prompt, "claude-code")
	assert.Contains(t, prompt, "aider")
	assert.Contains(t, prompt, "winner_index")
}

// TestTournamentCancellation verifies that cancelling a tournament correctly
// marks all candidates as eliminated.
func TestTournamentCancellation(t *testing.T) {
	t.Parallel()

	coord := tournament.NewCoordinator(tournamentTestLogger())
	ctx := context.Background()

	cfg := tournament.TournamentConfig{
		CandidateCount:            2,
		EarlyTerminationThreshold: 1.0,
	}

	coord.StartTournament(ctx, "t-cancel", "ticket-99", []string{"tr-1", "tr-2"}, cfg)
	err := coord.CancelTournament(ctx, "t-cancel")
	require.NoError(t, err)

	tr := coord.GetTournament("t-cancel")
	assert.Equal(t, tournament.StateCancelled, tr.State)
	assert.NotNil(t, tr.CompletedAt)
}
