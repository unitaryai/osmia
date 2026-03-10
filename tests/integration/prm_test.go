//go:build integration

// Package integration_test contains integration tests that verify the PRM
// subsystem evaluates a stream of events and produces correct interventions.
package integration_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/prm"
)

// prmTestLogger returns a logger suitable for PRM integration tests.
func prmTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func makePRMToolEvent(tool string, ts time.Time) *agentstream.StreamEvent {
	return &agentstream.StreamEvent{
		Type:      agentstream.EventToolCall,
		Timestamp: ts,
		Parsed:    &agentstream.ToolCallEvent{Tool: tool},
	}
}

// TestPRMProductiveStreamProducesContinue verifies that a productive
// sequence of tool calls results in continue interventions.
func TestPRMProductiveStreamProducesContinue(t *testing.T) {
	t.Parallel()

	cfg := prm.DefaultConfig()
	cfg.EvaluationInterval = 3
	eval := prm.NewEvaluator(cfg, prmTestLogger())

	ctx := context.Background()
	now := time.Now()

	// Feed a productive cycle: Read → Edit → Bash repeated.
	productiveTools := []string{"Read", "Edit", "Bash", "Read", "Edit", "Bash"}
	var lastIntervention *prm.Intervention
	for i, tool := range productiveTools {
		iv := eval.ProcessEvent(ctx, makePRMToolEvent(tool, now.Add(time.Duration(i)*time.Second)))
		if iv != nil {
			lastIntervention = iv
		}
	}

	require.NotNil(t, lastIntervention, "should have produced at least one evaluation")
	assert.Equal(t, prm.ActionContinue, lastIntervention.Action,
		"productive stream should result in continue action")
}

// TestPRMRepetitiveStreamTriggersIntervention verifies that a repetitive
// tool call pattern eventually triggers a nudge or escalation.
func TestPRMRepetitiveStreamTriggersIntervention(t *testing.T) {
	t.Parallel()

	cfg := prm.DefaultConfig()
	cfg.EvaluationInterval = 3
	cfg.WindowSize = 5
	cfg.ScoreThresholdNudge = 7
	cfg.ScoreThresholdEscalate = 3
	eval := prm.NewEvaluator(cfg, prmTestLogger())

	ctx := context.Background()
	now := time.Now()

	// Feed highly repetitive tool calls to drive the score down.
	var interventions []*prm.Intervention
	for i := 0; i < 30; i++ {
		iv := eval.ProcessEvent(ctx, makePRMToolEvent("Bash", now.Add(time.Duration(i)*time.Second)))
		if iv != nil {
			interventions = append(interventions, iv)
		}
	}

	require.NotEmpty(t, interventions, "should have produced evaluations")

	// At least the later evaluations should have low scores causing interventions.
	traj := eval.Trajectory()
	assert.GreaterOrEqual(t, traj.Len(), 3, "should have multiple trajectory points")

	// The latest score should be low given the repetitive pattern.
	latest := traj.Latest()
	require.NotNil(t, latest)
	assert.LessOrEqual(t, latest.Score, 5,
		"repetitive pattern should produce low scores")
}

// TestPRMForwarderIntegration verifies that the PRM evaluator is correctly
// invoked when wired into the Forwarder via WithEventProcessor.
func TestPRMForwarderIntegration(t *testing.T) {
	t.Parallel()

	cfg := prm.DefaultConfig()
	cfg.EvaluationInterval = 2
	eval := prm.NewEvaluator(cfg, prmTestLogger())

	// Build a StreamEventProcessor that feeds events into the PRM evaluator.
	processor := func(ctx context.Context, event *agentstream.StreamEvent) {
		eval.ProcessEvent(ctx, event)
	}

	fwd := agentstream.NewForwarder(
		prmTestLogger(),
		agentstream.WithEventProcessor(processor),
	)

	eventCh := make(chan *agentstream.StreamEvent, 20)
	now := time.Now()

	// Send a mix of events through the forwarder.
	events := []*agentstream.StreamEvent{
		makePRMToolEvent("Read", now),
		makePRMToolEvent("Edit", now.Add(1*time.Second)),
		makePRMToolEvent("Bash", now.Add(2*time.Second)),
		makePRMToolEvent("Read", now.Add(3*time.Second)),
	}

	for _, ev := range events {
		eventCh <- ev
	}
	close(eventCh)

	ctx := context.Background()
	err := fwd.Forward(ctx, eventCh)
	require.NoError(t, err)

	// The evaluator should have been invoked (interval=2, 4 events = 2 evaluations).
	assert.Equal(t, 4, eval.ToolCallCount(),
		"all tool call events should have been processed by PRM")
}

// TestPRMTrajectoryPatternDetection verifies that the trajectory correctly
// detects sustained decline across multiple evaluation cycles.
func TestPRMTrajectoryPatternDetection(t *testing.T) {
	t.Parallel()

	traj := prm.NewTrajectory(50)

	// Simulate a declining sequence of scores.
	scores := []int{9, 8, 6, 4, 2}
	for _, s := range scores {
		traj.AddScore(prm.StepScore{
			Score:     s,
			Reasoning: "test",
			Timestamp: time.Now(),
		})
	}

	assert.Equal(t, prm.PatternSustainedDecline, traj.Pattern(),
		"should detect sustained decline")
	assert.Equal(t, prm.TrendDeclining, traj.CurrentTrend(),
		"current trend should be declining")
}

// TestPRMEndToEndEscalation verifies the complete flow from repetitive
// events through scoring, trajectory detection, to escalation decision.
func TestPRMEndToEndEscalation(t *testing.T) {
	t.Parallel()

	cfg := prm.DefaultConfig()
	cfg.EvaluationInterval = 1
	cfg.WindowSize = 3
	cfg.ScoreThresholdNudge = 7
	cfg.ScoreThresholdEscalate = 3
	eval := prm.NewEvaluator(cfg, prmTestLogger())

	ctx := context.Background()
	now := time.Now()

	// Feed many repetitive events to build up a declining trajectory.
	var sawNonContinue bool
	for i := 0; i < 50; i++ {
		iv := eval.ProcessEvent(ctx, makePRMToolEvent("Bash", now.Add(time.Duration(i)*time.Second)))
		if iv != nil && iv.Action != prm.ActionContinue {
			sawNonContinue = true
		}
	}

	// With 50 identical tool calls, the scorer should consistently produce
	// low scores, eventually triggering at least a nudge.
	assert.True(t, sawNonContinue,
		"50 repetitive tool calls should trigger at least one non-continue intervention")
}
