package prm

import (
	"context"
	"log/slog"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/metrics"
)

// Config holds the configuration for the PRM evaluator.
type Config struct {
	// Enabled controls whether PRM evaluation is active.
	Enabled bool
	// EvaluationInterval is the number of tool calls between evaluations.
	EvaluationInterval int
	// WindowSize is the number of recent tool calls to consider per evaluation.
	WindowSize int
	// ScoreThresholdNudge is the minimum score to avoid a nudge intervention.
	ScoreThresholdNudge int
	// ScoreThresholdEscalate is the score at or below which escalation fires.
	ScoreThresholdEscalate int
	// HintFilePath is where nudge hints are written.
	HintFilePath string
	// MaxTrajectoryLength caps the number of scores retained.
	MaxTrajectoryLength int
}

// DefaultConfig returns a Config with sensible default values.
func DefaultConfig() Config {
	return Config{
		Enabled:                true,
		EvaluationInterval:     5,
		WindowSize:             10,
		ScoreThresholdNudge:    7,
		ScoreThresholdEscalate: 3,
		HintFilePath:           "/workspace/.osmia-hint.md",
		MaxTrajectoryLength:    50,
	}
}

// Evaluator is the main entry point for the PRM subsystem. It accumulates
// stream events, periodically scores agent behaviour, tracks the score
// trajectory, and decides whether intervention is needed.
type Evaluator struct {
	config     Config
	logger     *slog.Logger
	scorer     *Scorer
	trajectory *Trajectory
	decider    *InterventionDecider

	// eventBuffer accumulates tool call events between evaluations.
	eventBuffer []*agentstream.StreamEvent
	// toolCallCount tracks the total number of tool calls seen.
	toolCallCount int
}

// NewEvaluator creates a fully wired Evaluator from the given configuration.
func NewEvaluator(cfg Config, logger *slog.Logger) *Evaluator {
	return &Evaluator{
		config:     cfg,
		logger:     logger,
		scorer:     NewScorer(logger, cfg.WindowSize),
		trajectory: NewTrajectory(cfg.MaxTrajectoryLength),
		decider:    NewInterventionDecider(cfg.ScoreThresholdNudge, cfg.ScoreThresholdEscalate, cfg.HintFilePath),
	}
}

// ProcessEvent handles a single stream event. It buffers tool call events
// and triggers evaluation at the configured interval. Non-tool-call events
// are ignored. Returns an Intervention when one is recommended, or nil.
func (e *Evaluator) ProcessEvent(ctx context.Context, event *agentstream.StreamEvent) *Intervention {
	if !e.config.Enabled {
		return nil
	}

	if event == nil || event.Type != agentstream.EventToolCall {
		return nil
	}

	// Check context cancellation.
	select {
	case <-ctx.Done():
		return nil
	default:
	}

	e.eventBuffer = append(e.eventBuffer, event)
	e.toolCallCount++

	// Only evaluate at the configured interval.
	if e.toolCallCount%e.config.EvaluationInterval != 0 {
		return nil
	}

	return e.evaluate()
}

// evaluate runs a single scoring cycle and returns any intervention.
func (e *Evaluator) evaluate() *Intervention {
	// Take the most recent windowSize events from the buffer.
	window := e.eventBuffer
	if len(window) > e.config.WindowSize {
		window = window[len(window)-e.config.WindowSize:]
	}

	score := e.scorer.ScoreStep(window)
	e.trajectory.AddScore(*score)

	e.logger.Info("prm evaluation completed",
		"score", score.Score,
		"reasoning", score.Reasoning,
		"pattern", e.trajectory.Pattern(),
		"trend", e.trajectory.CurrentTrend(),
		"tool_calls_total", e.toolCallCount,
	)

	// Record metrics.
	metrics.PRMStepScores.Observe(float64(score.Score))
	metrics.PRMTrajectoryPatternsTotal.WithLabelValues(string(e.trajectory.Pattern())).Inc()

	intervention := e.decider.Decide(e.trajectory)

	if intervention.Action != ActionContinue {
		metrics.PRMInterventionsTotal.WithLabelValues(string(intervention.Action)).Inc()
		e.logger.Warn("prm intervention recommended",
			"action", intervention.Action,
			"reason", intervention.Reason,
			"score", score.Score,
		)
	}

	// Trim the buffer to avoid unbounded growth, keeping the window.
	if len(e.eventBuffer) > e.config.WindowSize {
		e.eventBuffer = e.eventBuffer[len(e.eventBuffer)-e.config.WindowSize:]
	}

	return intervention
}

// Trajectory returns the evaluator's trajectory for external inspection.
func (e *Evaluator) Trajectory() *Trajectory {
	return e.trajectory
}

// ToolCallCount returns the total number of tool calls processed.
func (e *Evaluator) ToolCallCount() int {
	return e.toolCallCount
}

// HintFilePath returns the configured path for hint files.
func (e *Evaluator) HintFilePath() string {
	return e.decider.HintFilePath()
}
