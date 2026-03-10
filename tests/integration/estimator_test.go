//go:build integration

package integration_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/estimator"
)

// TestEstimatorEndToEnd records 10 outcomes and verifies predictions
// fall within a reasonable range for a similar new task.
func TestEstimatorEndToEnd(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	store := estimator.NewMemoryEstimatorStore()
	scorer := estimator.NewComplexityScorer()
	cfg := &config.EstimatorConfig{
		Enabled:                true,
		MaxPredictedCostPerJob: 50.0,
		DefaultCostPerEngine: map[string]config.CostRange{
			"claude-code": {Low: 1.0, High: 8.0},
		},
		DefaultDurationPerEngine: map[string]config.DurationRange{
			"claude-code": {LowMinutes: 10, HighMinutes: 60},
		},
	}

	predictor := estimator.NewPredictor(store, cfg, logger)

	// Record 10 historical outcomes with varying complexity and costs.
	historicalTasks := []struct {
		desc     string
		taskType string
		repoSize int
		labels   []string
		cost     float64
		duration time.Duration
		engine   string
	}{
		{"Fix typo in readme", "typo_fix", 50, []string{"docs"}, 0.50, 5 * time.Minute, "claude-code"},
		{"Fix login bug", "bug_fix", 200, []string{"bug"}, 3.00, 25 * time.Minute, "claude-code"},
		{"Add email validation", "enhancement", 300, []string{"enhancement"}, 5.00, 40 * time.Minute, "claude-code"},
		{"Refactor auth module", "refactor", 500, []string{"refactor"}, 8.00, 60 * time.Minute, "claude-code"},
		{"Fix CSS alignment", "bug_fix", 100, []string{"bug"}, 1.50, 10 * time.Minute, "claude-code"},
		{"Add API endpoint", "new_feature", 400, []string{"feature"}, 6.00, 45 * time.Minute, "claude-code"},
		{"Update dependencies", "enhancement", 200, []string{"enhancement"}, 2.00, 15 * time.Minute, "claude-code"},
		{"Fix race condition", "bug_fix", 800, []string{"bug", "security"}, 10.00, 80 * time.Minute, "claude-code"},
		{"Add unit tests", "test", 300, []string{"test"}, 4.00, 35 * time.Minute, "claude-code"},
		{"Migrate database schema", "migration", 1000, []string{"migration"}, 12.00, 90 * time.Minute, "claude-code"},
	}

	for i, ht := range historicalTasks {
		score, err := scorer.Score(ctx, estimator.ComplexityInput{
			TaskDescription: ht.desc,
			TaskType:        ht.taskType,
			RepoSize:        ht.repoSize,
			Labels:          ht.labels,
		})
		require.NoError(t, err)

		err = predictor.RecordOutcome(ctx, estimator.PredictionOutcome{
			ComplexityScore: *score,
			Engine:          ht.engine,
			ActualCost:      ht.cost,
			ActualDuration:  ht.duration,
			Success:         true,
			TaskRunID:       "tr-hist-" + string(rune('A'+i)),
			RecordedAt:      time.Now(),
		})
		require.NoError(t, err)
	}

	// Test 1: Predict for a new bug fix (should be in the $1-10 range).
	bugFixScore, err := scorer.Score(ctx, estimator.ComplexityInput{
		TaskDescription: "Fix a null pointer when user has no avatar",
		TaskType:        "bug_fix",
		RepoSize:        250,
		Labels:          []string{"bug"},
	})
	require.NoError(t, err)

	pred, err := predictor.Predict(ctx, *bugFixScore, "claude-code")
	require.NoError(t, err)
	assert.Greater(t, pred.EstimatedCostLow, 0.0)
	assert.Less(t, pred.EstimatedCostHigh, 20.0,
		"bug fix prediction should be reasonable")
	assert.Greater(t, pred.Confidence, 0.0)

	// Test 2: Predict for a migration (high complexity).
	migrationScore, err := scorer.Score(ctx, estimator.ComplexityInput{
		TaskDescription: "Migrate the payment processor from Stripe v2 to v3 API",
		TaskType:        "migration",
		RepoSize:        1200,
		Labels:          []string{"migration"},
	})
	require.NoError(t, err)

	migrationPred, err := predictor.Predict(ctx, *migrationScore, "claude-code")
	require.NoError(t, err)
	assert.Greater(t, migrationPred.EstimatedCostHigh, pred.EstimatedCostHigh,
		"migration should be predicted as more expensive than a bug fix")

	// Test 3: Auto-reject guard rail.
	assert.False(t, predictor.ShouldAutoReject(pred),
		"bug fix should not be auto-rejected")

	expensivePred := &estimator.Prediction{
		EstimatedCostLow:  30.0,
		EstimatedCostHigh: 60.0,
	}
	assert.True(t, predictor.ShouldAutoReject(expensivePred),
		"expensive prediction should be auto-rejected (threshold $50)")
}
