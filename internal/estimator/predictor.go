package estimator

import (
	"context"
	"log/slog"
	"sort"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/metrics"
)

const defaultK = 5

// Prediction holds the estimated cost and duration ranges for a task.
type Prediction struct {
	EstimatedCostLow      float64 `json:"estimated_cost_low"`      // USD, P25
	EstimatedCostHigh     float64 `json:"estimated_cost_high"`     // USD, P75
	EstimatedDurationLow  int     `json:"estimated_duration_low"`  // minutes, P25
	EstimatedDurationHigh int     `json:"estimated_duration_high"` // minutes, P75
	Confidence            float64 `json:"confidence"`              // 0-1, based on sample count
}

// Predictor uses historical task outcomes to estimate cost and duration
// for new tasks via k-nearest-neighbour lookup.
type Predictor struct {
	store  EstimatorStore
	cfg    *config.EstimatorConfig
	logger *slog.Logger
}

// NewPredictor creates a predictor backed by the given store.
func NewPredictor(store EstimatorStore, cfg *config.EstimatorConfig, logger *slog.Logger) *Predictor {
	return &Predictor{
		store:  store,
		cfg:    cfg,
		logger: logger,
	}
}

// Predict estimates cost and duration for a task with the given complexity
// score being executed on the specified engine. When insufficient historical
// data is available, engine-specific cold-start defaults from config are used.
func (p *Predictor) Predict(ctx context.Context, score ComplexityScore, engine string) (*Prediction, error) {
	metrics.EstimatorPredictionsTotal.Inc()

	neighbours, err := p.store.QuerySimilar(ctx, score, engine, defaultK)
	if err != nil {
		return nil, err
	}

	// If we have enough neighbours, use kNN aggregation.
	if len(neighbours) >= 2 {
		pred := aggregateNeighbours(neighbours)
		metrics.EstimatorPredictedCost.Observe((pred.EstimatedCostLow + pred.EstimatedCostHigh) / 2)

		p.logger.Info("prediction from historical data",
			"engine", engine,
			"neighbours", len(neighbours),
			"cost_range", [2]float64{pred.EstimatedCostLow, pred.EstimatedCostHigh},
			"duration_range", [2]int{pred.EstimatedDurationLow, pred.EstimatedDurationHigh},
			"confidence", pred.Confidence,
		)

		return pred, nil
	}

	// Cold start: use engine defaults from config.
	pred := p.coldStartPrediction(engine, score)
	metrics.EstimatorPredictedCost.Observe((pred.EstimatedCostLow + pred.EstimatedCostHigh) / 2)

	p.logger.Info("cold-start prediction (insufficient history)",
		"engine", engine,
		"cost_range", [2]float64{pred.EstimatedCostLow, pred.EstimatedCostHigh},
	)

	return pred, nil
}

// RecordOutcome feeds an actual task result back into the store for
// future predictions.
func (p *Predictor) RecordOutcome(ctx context.Context, outcome PredictionOutcome) error {
	return p.store.SaveOutcome(ctx, outcome)
}

// ShouldAutoReject returns true if the predicted cost exceeds the
// configured maximum per job.
func (p *Predictor) ShouldAutoReject(pred *Prediction) bool {
	if p.cfg.MaxPredictedCostPerJob <= 0 {
		return false
	}
	// Reject if even the low estimate exceeds the threshold.
	if pred.EstimatedCostHigh > p.cfg.MaxPredictedCostPerJob {
		metrics.EstimatorAutoRejectionsTotal.Inc()
		return true
	}
	return false
}

// aggregateNeighbours computes P25/P75 ranges from the k nearest neighbours.
func aggregateNeighbours(neighbours []PredictionOutcome) *Prediction {
	costs := make([]float64, len(neighbours))
	durations := make([]int, len(neighbours))

	for i, n := range neighbours {
		costs[i] = n.ActualCost
		durations[i] = int(n.ActualDuration.Minutes())
	}

	sort.Float64s(costs)
	sort.Ints(durations)

	n := len(neighbours)
	p25Idx := n / 4
	p75Idx := (3 * n) / 4
	if p75Idx >= n {
		p75Idx = n - 1
	}

	// Confidence increases with sample count, capping at 1.0.
	confidence := float64(n) / float64(defaultK)
	if confidence > 1.0 {
		confidence = 1.0
	}

	return &Prediction{
		EstimatedCostLow:      costs[p25Idx],
		EstimatedCostHigh:     costs[p75Idx],
		EstimatedDurationLow:  durations[p25Idx],
		EstimatedDurationHigh: durations[p75Idx],
		Confidence:            confidence,
	}
}

// coldStartPrediction uses engine-specific defaults from configuration,
// scaled by the overall complexity score.
func (p *Predictor) coldStartPrediction(engine string, score ComplexityScore) *Prediction {
	costLow := 1.0
	costHigh := 5.0
	durLow := 10
	durHigh := 60

	if p.cfg != nil {
		if cr, ok := p.cfg.DefaultCostPerEngine[engine]; ok {
			costLow = cr.Low
			costHigh = cr.High
		}
		if dr, ok := p.cfg.DefaultDurationPerEngine[engine]; ok {
			durLow = dr.LowMinutes
			durHigh = dr.HighMinutes
		}
	}

	// Scale by complexity: higher complexity shifts estimates upward.
	scaleFactor := 0.5 + score.Overall
	costLow *= scaleFactor
	costHigh *= scaleFactor
	durLow = int(float64(durLow) * scaleFactor)
	durHigh = int(float64(durHigh) * scaleFactor)

	return &Prediction{
		EstimatedCostLow:      costLow,
		EstimatedCostHigh:     costHigh,
		EstimatedDurationLow:  durLow,
		EstimatedDurationHigh: durHigh,
		Confidence:            0.1, // low confidence for cold-start
	}
}
