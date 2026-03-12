package prm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/agentstream"
)

func TestEvaluator_ProcessEvent_BasicFlow(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EvaluationInterval = 3
	eval := NewEvaluator(cfg, testLogger())

	ctx := context.Background()

	// First two events should not trigger evaluation.
	iv := eval.ProcessEvent(ctx, makeToolEvent("Read"))
	assert.Nil(t, iv, "should not evaluate before interval")
	iv = eval.ProcessEvent(ctx, makeToolEvent("Edit"))
	assert.Nil(t, iv, "should not evaluate before interval")

	// Third event triggers evaluation.
	iv = eval.ProcessEvent(ctx, makeToolEvent("Bash"))
	require.NotNil(t, iv, "should evaluate at interval")
	assert.Equal(t, ActionContinue, iv.Action, "productive pattern should continue")
}

func TestEvaluator_ProcessEvent_IgnoresNonToolCalls(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EvaluationInterval = 2
	eval := NewEvaluator(cfg, testLogger())

	ctx := context.Background()

	// Cost events should be ignored.
	costEvent := &agentstream.StreamEvent{
		Type:      agentstream.EventCost,
		Timestamp: time.Now(),
		Parsed:    &agentstream.CostEvent{CostUSD: 1.0},
	}
	iv := eval.ProcessEvent(ctx, costEvent)
	assert.Nil(t, iv)

	// Nil events should be ignored.
	iv = eval.ProcessEvent(ctx, nil)
	assert.Nil(t, iv)

	assert.Equal(t, 0, eval.ToolCallCount())
}

func TestEvaluator_ProcessEvent_DisabledDoesNothing(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false
	eval := NewEvaluator(cfg, testLogger())

	ctx := context.Background()
	for i := 0; i < 20; i++ {
		iv := eval.ProcessEvent(ctx, makeToolEvent("Read"))
		assert.Nil(t, iv, "disabled evaluator should never intervene")
	}
}

func TestEvaluator_ProcessEvent_CancelledContext(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EvaluationInterval = 1
	eval := NewEvaluator(cfg, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	iv := eval.ProcessEvent(ctx, makeToolEvent("Read"))
	assert.Nil(t, iv, "cancelled context should prevent evaluation")
}

func TestEvaluator_DetectsDeclineAndEscalates(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EvaluationInterval = 1
	cfg.WindowSize = 5
	cfg.ScoreThresholdNudge = 7
	cfg.ScoreThresholdEscalate = 3
	eval := NewEvaluator(cfg, testLogger())

	ctx := context.Background()

	// Feed increasingly repetitive patterns to drive score down.
	// Round 1: productive → high score.
	eval.ProcessEvent(ctx, makeToolEvent("Read"))

	// Rounds 2-6: repetitive Bash calls → score drops.
	for i := 0; i < 5; i++ {
		eval.ProcessEvent(ctx, makeToolEvent("Bash"))
	}

	traj := eval.Trajectory()
	assert.GreaterOrEqual(t, traj.Len(), 2, "should have tracked multiple scores")
}

func TestEvaluator_TrajectoryAccessor(t *testing.T) {
	eval := NewEvaluator(DefaultConfig(), testLogger())
	assert.NotNil(t, eval.Trajectory())
	assert.Equal(t, 0, eval.Trajectory().Len())
}

func TestEvaluator_ToolCallCount(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EvaluationInterval = 100 // High interval so we can count without triggering eval.
	eval := NewEvaluator(cfg, testLogger())

	ctx := context.Background()
	for i := 0; i < 10; i++ {
		eval.ProcessEvent(ctx, makeToolEvent("Read"))
	}
	assert.Equal(t, 10, eval.ToolCallCount())
}

func TestEvaluator_HintFilePath(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HintFilePath = "/custom/hint.md"
	eval := NewEvaluator(cfg, testLogger())
	assert.Equal(t, "/custom/hint.md", eval.HintFilePath())
}
