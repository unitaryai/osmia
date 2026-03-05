package tournament

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestCoordinator_StartTournament(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		candidates []string
		wantErr    bool
	}{
		{
			name:       "valid tournament with 2 candidates",
			id:         "t-1",
			candidates: []string{"tr-1", "tr-2"},
		},
		{
			name:       "valid tournament with 3 candidates",
			id:         "t-2",
			candidates: []string{"tr-1", "tr-2", "tr-3"},
		},
		{
			name:       "too few candidates",
			id:         "t-3",
			candidates: []string{"tr-1"},
			wantErr:    true,
		},
		{
			name:       "empty tournament ID",
			id:         "",
			candidates: []string{"tr-1", "tr-2"},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCoordinator(testLogger())
			cfg := TournamentConfig{
				CandidateCount:            len(tt.candidates),
				EarlyTerminationThreshold: 0.6,
				JudgeEngine:               "claude-code",
			}

			tournament, err := c.StartTournament(context.Background(), tt.id, "ticket-1", tt.candidates, cfg)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.id, tournament.ID)
			assert.Equal(t, StateCompeting, tournament.State)
			assert.Len(t, tournament.TaskRunIDs, len(tt.candidates))

			for _, id := range tt.candidates {
				assert.Equal(t, StateCompeting, tournament.CandidateStates[id])
			}
		})
	}
}

func TestCoordinator_StartTournament_DuplicateID(t *testing.T) {
	c := NewCoordinator(testLogger())
	cfg := TournamentConfig{CandidateCount: 2}

	_, err := c.StartTournament(context.Background(), "t-1", "ticket-1", []string{"tr-1", "tr-2"}, cfg)
	require.NoError(t, err)

	_, err = c.StartTournament(context.Background(), "t-1", "ticket-2", []string{"tr-3", "tr-4"}, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestCoordinator_OnCandidateComplete(t *testing.T) {
	c := NewCoordinator(testLogger())
	cfg := TournamentConfig{
		CandidateCount:            3,
		EarlyTerminationThreshold: 0.6,
	}

	_, err := c.StartTournament(context.Background(), "t-1", "ticket-1", []string{"tr-1", "tr-2", "tr-3"}, cfg)
	require.NoError(t, err)

	// First candidate: not enough yet.
	ready, err := c.OnCandidateComplete(context.Background(), "t-1", &CandidateResult{
		TaskRunID: "tr-1",
		Engine:    "claude-code",
		Success:   true,
		Cost:      1.0,
		Duration:  3 * time.Minute,
	})
	require.NoError(t, err)
	assert.False(t, ready, "should not be ready after 1 of 3")

	// Second candidate: 60% threshold met (2/3 >= 0.6*3 rounded up to 2).
	ready, err = c.OnCandidateComplete(context.Background(), "t-1", &CandidateResult{
		TaskRunID: "tr-2",
		Engine:    "aider",
		Success:   true,
		Cost:      0.5,
		Duration:  2 * time.Minute,
	})
	require.NoError(t, err)
	assert.True(t, ready, "should be ready after 2 of 3 at 60% threshold")
}

func TestCoordinator_OnCandidateComplete_InvalidTournament(t *testing.T) {
	c := NewCoordinator(testLogger())

	_, err := c.OnCandidateComplete(context.Background(), "nonexistent", &CandidateResult{TaskRunID: "tr-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCoordinator_OnCandidateComplete_UnknownCandidate(t *testing.T) {
	c := NewCoordinator(testLogger())
	cfg := TournamentConfig{CandidateCount: 2}

	_, err := c.StartTournament(context.Background(), "t-1", "ticket-1", []string{"tr-1", "tr-2"}, cfg)
	require.NoError(t, err)

	_, err = c.OnCandidateComplete(context.Background(), "t-1", &CandidateResult{TaskRunID: "tr-unknown"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a candidate")
}

func TestCoordinator_FullLifecycle(t *testing.T) {
	c := NewCoordinator(testLogger())
	ctx := context.Background()
	cfg := TournamentConfig{
		CandidateCount:            3,
		EarlyTerminationThreshold: 0.6,
		JudgeEngine:               "claude-code",
	}

	// Start tournament.
	tournament, err := c.StartTournament(ctx, "t-1", "ticket-1", []string{"tr-1", "tr-2", "tr-3"}, cfg)
	require.NoError(t, err)
	assert.Equal(t, StateCompeting, tournament.State)

	// Complete 2 of 3 candidates.
	_, err = c.OnCandidateComplete(ctx, "t-1", &CandidateResult{
		TaskRunID: "tr-1", Engine: "claude-code", Success: true, Cost: 2.0, Duration: 5 * time.Minute,
	})
	require.NoError(t, err)
	_, err = c.OnCandidateComplete(ctx, "t-1", &CandidateResult{
		TaskRunID: "tr-2", Engine: "aider", Success: true, Cost: 0.8, Duration: 3 * time.Minute,
	})
	require.NoError(t, err)

	// Begin judging.
	results, err := c.BeginJudging(ctx, "t-1", "judge-tr-1")
	require.NoError(t, err)
	assert.Len(t, results, 2)

	tournament = c.GetTournament("t-1")
	assert.Equal(t, StateJudging, tournament.State)
	assert.Equal(t, StateEliminated, tournament.CandidateStates["tr-3"])

	// Lagging candidates should include tr-3.
	lagging := c.LaggingCandidates("t-1")
	assert.Equal(t, []string{"tr-3"}, lagging)

	// Select winner.
	err = c.SelectWinner(ctx, "t-1", "tr-2")
	require.NoError(t, err)

	tournament = c.GetTournament("t-1")
	assert.Equal(t, StateSelected, tournament.State)
	assert.Equal(t, "tr-2", tournament.WinnerTaskRunID)
	assert.Equal(t, StateSelected, tournament.CandidateStates["tr-2"])
	assert.Equal(t, StateEliminated, tournament.CandidateStates["tr-1"])
	assert.NotNil(t, tournament.CompletedAt)
}

func TestCoordinator_SelectWinner_InvalidState(t *testing.T) {
	c := NewCoordinator(testLogger())
	ctx := context.Background()
	cfg := TournamentConfig{CandidateCount: 2}

	_, err := c.StartTournament(ctx, "t-1", "ticket-1", []string{"tr-1", "tr-2"}, cfg)
	require.NoError(t, err)

	// Cannot select winner while still competing.
	err := c.SelectWinner(ctx, "t-1", "tr-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in judging state")
}

func TestCoordinator_CancelTournament(t *testing.T) {
	c := NewCoordinator(testLogger())
	ctx := context.Background()
	cfg := TournamentConfig{CandidateCount: 2}

	_, err := c.StartTournament(ctx, "t-1", "ticket-1", []string{"tr-1", "tr-2"}, cfg)
	require.NoError(t, err)

	err := c.CancelTournament(ctx, "t-1")
	require.NoError(t, err)

	tournament := c.GetTournament("t-1")
	assert.Equal(t, StateCancelled, tournament.State)
	assert.NotNil(t, tournament.CompletedAt)
	assert.Equal(t, StateEliminated, tournament.CandidateStates["tr-1"])
	assert.Equal(t, StateEliminated, tournament.CandidateStates["tr-2"])
}

func TestCoordinator_GetTournament_NotFound(t *testing.T) {
	c := NewCoordinator(testLogger())
	assert.Nil(t, c.GetTournament("nonexistent"))
}
