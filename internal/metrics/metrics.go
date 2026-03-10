// Package metrics defines Prometheus metrics for the Osmia controller.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const namespace = "osmia"

// Core controller metrics.
var (
	// TaskRunsTotal counts the total number of task runs by final state.
	TaskRunsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "taskruns_total",
			Help:      "Total number of task runs by state.",
		},
		[]string{"state"},
	)

	// TaskRunDurationSeconds tracks the duration of task runs in seconds.
	TaskRunDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "taskrun_duration_seconds",
			Help:      "Duration of task runs in seconds.",
			Buckets:   prometheus.ExponentialBuckets(60, 2, 8), // 1m to ~4h
		},
		[]string{"engine"},
	)

	// ActiveJobs tracks the number of currently active jobs.
	ActiveJobs = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "active_jobs",
			Help:      "Number of currently active jobs.",
		},
	)

	// PluginErrorsTotal counts plugin errors by plugin name.
	PluginErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "plugin_errors_total",
			Help:      "Total number of plugin errors by plugin.",
		},
		[]string{"plugin"},
	)
)

// PRM (Process Reward Model) metrics.
var (
	// PRMStepScores tracks the distribution of PRM step scores.
	PRMStepScores = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "prm",
			Name:      "step_scores",
			Help:      "Distribution of PRM step scores (1-10).",
			Buckets:   prometheus.LinearBuckets(1, 1, 10),
		},
	)

	// PRMInterventionsTotal counts PRM interventions by action type.
	PRMInterventionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "prm",
			Name:      "interventions_total",
			Help:      "Total number of PRM interventions by action type.",
		},
		[]string{"action"},
	)

	// PRMTrajectoryPatternsTotal counts detected trajectory patterns.
	PRMTrajectoryPatternsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "prm",
			Name:      "trajectory_patterns_total",
			Help:      "Total number of detected trajectory patterns by type.",
		},
		[]string{"pattern"},
	)
)

// Routing metrics track intelligent engine selection behaviour.
var (
	// RoutingEngineSelectedTotal counts how many times each engine was
	// selected as the primary choice by the intelligent router.
	RoutingEngineSelectedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "routing",
			Name:      "engine_selected_total",
			Help:      "Total times each engine was selected as primary by intelligent routing.",
		},
		[]string{"engine"},
	)

	// RoutingExplorationTotal counts how many times epsilon-greedy
	// exploration overrode the best-scoring engine.
	RoutingExplorationTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "routing",
			Name:      "exploration_total",
			Help:      "Total times epsilon-greedy exploration chose a random engine.",
		},
	)

	// RoutingFingerprintSamples tracks the number of recorded task outcomes
	// per engine.
	RoutingFingerprintSamples = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "routing",
			Name:      "fingerprint_samples",
			Help:      "Number of task outcome samples recorded per engine.",
		},
		[]string{"engine"},
	)

	// RoutingSuccessRate tracks the Laplace-smoothed success rate per
	// engine, dimension, and value.
	RoutingSuccessRate = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "routing",
			Name:      "success_rate",
			Help:      "Laplace-smoothed success rate by engine, dimension, and value.",
		},
		[]string{"engine", "dimension", "value"},
	)
)

// Tournament metrics track competitive execution behaviour.
var (
	// TournamentTotal counts the total number of tournaments started.
	TournamentTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "tournament",
			Name:      "total",
			Help:      "Total number of tournaments started.",
		},
	)

	// TournamentCandidatesTotal counts completed candidates by engine.
	TournamentCandidatesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "tournament",
			Name:      "candidates_total",
			Help:      "Total tournament candidates completed by engine.",
		},
		[]string{"engine"},
	)

	// TournamentWinnerEngine counts which engines win tournaments.
	TournamentWinnerEngine = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "tournament",
			Name:      "winner_engine_total",
			Help:      "Total tournament wins by engine.",
		},
		[]string{"engine"},
	)

	// TournamentCostTotal tracks the total cost of tournaments (all candidates + judge).
	TournamentCostTotal = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "tournament",
			Name:      "cost_total",
			Help:      "Total cost of tournaments in USD (all candidates + judge).",
			Buckets:   prometheus.ExponentialBuckets(1, 2, 10), // $1 to ~$512
		},
	)

	// TournamentDurationSeconds tracks tournament duration from start to winner selection.
	TournamentDurationSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "tournament",
			Name:      "duration_seconds",
			Help:      "Duration of tournaments from start to winner selection in seconds.",
			Buckets:   prometheus.ExponentialBuckets(60, 2, 8), // 1m to ~4h
		},
	)
)

// Estimator metrics track predictive cost and duration estimation.
var (
	// EstimatorPredictionsTotal counts the total number of predictions made.
	EstimatorPredictionsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "estimator",
			Name:      "predictions_total",
			Help:      "Total number of cost/duration predictions made.",
		},
	)

	// EstimatorPredictedCost tracks the distribution of predicted costs.
	EstimatorPredictedCost = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "estimator",
			Name:      "predicted_cost",
			Help:      "Distribution of predicted costs in USD.",
			Buckets:   prometheus.ExponentialBuckets(0.5, 2, 10), // $0.50 to ~$256
		},
	)

	// EstimatorAutoRejectionsTotal counts tasks rejected by predicted cost
	// exceeding the configured threshold.
	EstimatorAutoRejectionsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "estimator",
			Name:      "auto_rejections_total",
			Help:      "Total tasks auto-rejected due to predicted cost exceeding threshold.",
		},
	)

	// EstimatorPredictionAccuracy tracks the ratio of actual to predicted
	// cost, where values near 1.0 indicate accurate predictions.
	EstimatorPredictionAccuracy = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "estimator",
			Name:      "prediction_accuracy",
			Help:      "Ratio of actual cost to predicted cost midpoint (1.0 = perfect).",
			Buckets:   prometheus.LinearBuckets(0.0, 0.25, 9), // 0.0 to 2.0
		},
	)
)

// Watchdog adaptive calibration metrics.
var (
	// WatchdogCalibratedThreshold exposes the current calibrated threshold
	// values per signal, repo pattern, engine, and task type.
	WatchdogCalibratedThreshold = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "watchdog",
			Name:      "calibrated_threshold",
			Help:      "Current calibrated threshold value by signal and profile key.",
		},
		[]string{"signal", "repo_pattern", "engine", "task_type"},
	)

	// WatchdogCalibrationSamples tracks the number of observations collected
	// per profile key, indicating calibration confidence.
	WatchdogCalibrationSamples = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "watchdog",
			Name:      "calibration_samples",
			Help:      "Number of calibration samples collected per profile key.",
		},
		[]string{"repo_pattern", "engine", "task_type"},
	)

	// WatchdogCalibrationOverridesTotal counts the number of times a
	// calibrated threshold was used instead of a static default.
	WatchdogCalibrationOverridesTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "watchdog",
			Name:      "calibration_overrides_total",
			Help:      "Total number of times calibrated thresholds overrode static defaults.",
		},
	)
)

// Causal diagnosis metrics.
var (
	// DiagnosisTotal counts diagnoses by failure mode.
	DiagnosisTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "diagnosis",
			Name:      "total",
			Help:      "Total number of causal diagnoses by failure mode.",
		},
		[]string{"failure_mode"},
	)

	// DiagnosisEngineSwitchesTotal counts engine switches triggered by diagnosis.
	DiagnosisEngineSwitchesTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "diagnosis",
			Name:      "engine_switches_total",
			Help:      "Total number of engine switches recommended by causal diagnosis.",
		},
	)

	// DiagnosisRetrySuccessTotal counts retries that succeeded after diagnosis.
	DiagnosisRetrySuccessTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "diagnosis",
			Name:      "retry_success_total",
			Help:      "Total number of retries that succeeded after causal diagnosis.",
		},
	)
)

// Memory subsystem metrics.
var (
	// MemoryNodesTotal tracks the current number of nodes by type.
	MemoryNodesTotal = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "memory",
			Name:      "nodes_total",
			Help:      "Current number of knowledge graph nodes by type.",
		},
		[]string{"type"},
	)

	// MemoryQueriesTotal counts memory graph queries.
	MemoryQueriesTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "memory",
			Name:      "queries_total",
			Help:      "Total number of memory graph queries.",
		},
	)

	// MemoryExtractionsTotal counts knowledge extractions by outcome.
	MemoryExtractionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "memory",
			Name:      "extractions_total",
			Help:      "Total number of knowledge extractions by outcome.",
		},
		[]string{"outcome"},
	)

	// MemoryConfidenceDistribution tracks the distribution of node confidence values.
	MemoryConfidenceDistribution = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "memory",
			Name:      "confidence_distribution",
			Help:      "Distribution of knowledge graph node confidence values.",
			Buckets:   prometheus.LinearBuckets(0.0, 0.1, 11), // 0.0 to 1.0 in 0.1 steps
		},
	)
)
