package estimator

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestPredictor_ColdStart(t *testing.T) {
	store := NewMemoryEstimatorStore()
	cfg := &config.EstimatorConfig{
		Enabled: true,
		DefaultCostPerEngine: map[string]config.CostRange{
			"claude-code": {Low: 2.0, High: 10.0},
		},
		DefaultDurationPerEngine: map[string]config.DurationRange{
			"claude-code": {LowMinutes: 15, HighMinutes: 90},
		},
	}

	p := NewPredictor(store, cfg, testLogger())
	ctx := context.Background()

	score := ComplexityScore{
		Overall: 0.5,
		Dimensions: map[string]float64{
			DimDescriptionComplexity: 0.5,
			DimRepoSize:              0.5,
		},
	}

	pred, err := p.Predict(ctx, score, "claude-code")
	require.NoError(t, err)
	require.NotNil(t, pred)

	assert.Greater(t, pred.EstimatedCostLow, 0.0)
	assert.Greater(t, pred.EstimatedCostHigh, pred.EstimatedCostLow)
	assert.Greater(t, pred.EstimatedDurationLow, 0)
	assert.Greater(t, pred.EstimatedDurationHigh, pred.EstimatedDurationLow)
	assert.InDelta(t, 0.1, pred.Confidence, 0.01,
		"cold-start should have low confidence")
}

func TestPredictor_WithHistoricalData(t *testing.T) {
	store := NewMemoryEstimatorStore()
	ctx := context.Background()

	// Seed historical outcomes.
	for i := 0; i < 5; i++ {
		require.NoError(t, store.SaveOutcome(ctx, PredictionOutcome{
			ComplexityScore: ComplexityScore{
				Overall: 0.5,
				Dimensions: map[string]float64{
					DimDescriptionComplexity: 0.5,
					DimRepoSize:              0.5,
					DimTaskTypeComplexity:    0.5,
				},
			},
			Engine:         "claude-code",
			ActualCost:     float64(3 + i), // 3, 4, 5, 6, 7
			ActualDuration: time.Duration(20+i*10) * time.Minute,
			Success:        true,
			TaskRunID:      "tr-" + string(rune('A'+i)),
			RecordedAt:     time.Now(),
		}))
	}

	cfg := &config.EstimatorConfig{Enabled: true}
	p := NewPredictor(store, cfg, testLogger())

	score := ComplexityScore{
		Overall: 0.5,
		Dimensions: map[string]float64{
			DimDescriptionComplexity: 0.5,
			DimRepoSize:              0.5,
			DimTaskTypeComplexity:    0.5,
		},
	}

	pred, err := p.Predict(ctx, score, "claude-code")
	require.NoError(t, err)

	// With 5 outcomes ranging from $3-$7, P25=$3 and P75=$6 or close.
	assert.GreaterOrEqual(t, pred.EstimatedCostLow, 3.0)
	assert.LessOrEqual(t, pred.EstimatedCostHigh, 7.0)
	assert.Equal(t, 1.0, pred.Confidence, "5 of 5 neighbours should give full confidence")
}

func TestPredictor_ShouldAutoReject(t *testing.T) {
	tests := []struct {
		name       string
		maxCost    float64
		prediction Prediction
		expected   bool
	}{
		{
			name:    "below threshold",
			maxCost: 20.0,
			prediction: Prediction{
				EstimatedCostLow:  5.0,
				EstimatedCostHigh: 15.0,
			},
			expected: false,
		},
		{
			name:    "above threshold",
			maxCost: 10.0,
			prediction: Prediction{
				EstimatedCostLow:  5.0,
				EstimatedCostHigh: 15.0,
			},
			expected: true,
		},
		{
			name:    "threshold disabled (zero)",
			maxCost: 0,
			prediction: Prediction{
				EstimatedCostLow:  100.0,
				EstimatedCostHigh: 500.0,
			},
			expected: false,
		},
		{
			name:    "exactly at threshold",
			maxCost: 15.0,
			prediction: Prediction{
				EstimatedCostLow:  5.0,
				EstimatedCostHigh: 15.0,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.EstimatorConfig{
				MaxPredictedCostPerJob: tt.maxCost,
			}
			p := NewPredictor(nil, cfg, testLogger())
			assert.Equal(t, tt.expected, p.ShouldAutoReject(&tt.prediction))
		})
	}
}

func TestPredictor_RecordOutcome(t *testing.T) {
	store := NewMemoryEstimatorStore()
	cfg := &config.EstimatorConfig{Enabled: true}
	p := NewPredictor(store, cfg, testLogger())
	ctx := context.Background()

	err := p.RecordOutcome(ctx, PredictionOutcome{
		ComplexityScore: ComplexityScore{Overall: 0.5},
		Engine:          "claude-code",
		ActualCost:      5.0,
		ActualDuration:  30 * time.Minute,
		Success:         true,
		TaskRunID:       "tr-001",
		RecordedAt:      time.Now(),
	})
	require.NoError(t, err)

	// Verify it was stored.
	results, err := store.QuerySimilar(ctx, ComplexityScore{Overall: 0.5}, "claude-code", 10)
	require.NoError(t, err)
	assert.Len(t, results, 1)
}

func TestMemoryEstimatorStore_QuerySimilar(t *testing.T) {
	store := NewMemoryEstimatorStore()
	ctx := context.Background()

	// Add outcomes at different complexity levels.
	outcomes := []PredictionOutcome{
		{
			ComplexityScore: ComplexityScore{
				Overall:    0.2,
				Dimensions: map[string]float64{DimRepoSize: 0.2, DimTaskTypeComplexity: 0.2},
			},
			Engine: "claude-code", ActualCost: 2.0, ActualDuration: 10 * time.Minute,
			TaskRunID: "tr-1", RecordedAt: time.Now(),
		},
		{
			ComplexityScore: ComplexityScore{
				Overall:    0.5,
				Dimensions: map[string]float64{DimRepoSize: 0.5, DimTaskTypeComplexity: 0.5},
			},
			Engine: "claude-code", ActualCost: 5.0, ActualDuration: 30 * time.Minute,
			TaskRunID: "tr-2", RecordedAt: time.Now(),
		},
		{
			ComplexityScore: ComplexityScore{
				Overall:    0.9,
				Dimensions: map[string]float64{DimRepoSize: 0.9, DimTaskTypeComplexity: 0.9},
			},
			Engine: "claude-code", ActualCost: 15.0, ActualDuration: 90 * time.Minute,
			TaskRunID: "tr-3", RecordedAt: time.Now(),
		},
		{
			ComplexityScore: ComplexityScore{
				Overall:    0.5,
				Dimensions: map[string]float64{DimRepoSize: 0.5, DimTaskTypeComplexity: 0.5},
			},
			Engine: "aider", ActualCost: 3.0, ActualDuration: 20 * time.Minute,
			TaskRunID: "tr-4", RecordedAt: time.Now(),
		},
	}

	for _, o := range outcomes {
		require.NoError(t, store.SaveOutcome(ctx, o))
	}

	// Query for complexity ~0.5 on claude-code; should return tr-2 closest.
	results, err := store.QuerySimilar(ctx, ComplexityScore{
		Overall:    0.5,
		Dimensions: map[string]float64{DimRepoSize: 0.5, DimTaskTypeComplexity: 0.5},
	}, "claude-code", 2)
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "tr-2", results[0].TaskRunID, "nearest neighbour should be tr-2")

	// Filter by engine: aider only.
	results, err = store.QuerySimilar(ctx, ComplexityScore{
		Overall:    0.5,
		Dimensions: map[string]float64{DimRepoSize: 0.5, DimTaskTypeComplexity: 0.5},
	}, "aider", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "tr-4", results[0].TaskRunID)
}

func TestMemoryEstimatorStore_SaveOutcomeValidation(t *testing.T) {
	store := NewMemoryEstimatorStore()
	ctx := context.Background()

	err := store.SaveOutcome(ctx, PredictionOutcome{TaskRunID: ""})
	assert.Error(t, err, "should reject outcome without task run ID")
}

func TestEuclideanDistance(t *testing.T) {
	tests := []struct {
		name     string
		a        ComplexityScore
		b        ComplexityScore
		expected float64
	}{
		{
			name:     "identical scores",
			a:        ComplexityScore{Dimensions: map[string]float64{"x": 0.5}},
			b:        ComplexityScore{Dimensions: map[string]float64{"x": 0.5}},
			expected: 0.0,
		},
		{
			name:     "unit distance in one dimension",
			a:        ComplexityScore{Dimensions: map[string]float64{"x": 0.0}},
			b:        ComplexityScore{Dimensions: map[string]float64{"x": 1.0}},
			expected: 1.0,
		},
		{
			name:     "missing dimension treated as zero",
			a:        ComplexityScore{Dimensions: map[string]float64{"x": 0.5, "y": 0.5}},
			b:        ComplexityScore{Dimensions: map[string]float64{"x": 0.5}},
			expected: 0.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.expected, euclideanDistance(tt.a, tt.b), 0.0001)
		})
	}
}
