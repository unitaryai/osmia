package watchdog

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/robodev/internal/taskrun"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func runningTaskRun() *taskrun.TaskRun {
	tr := taskrun.New("tr-1", "idem-1", "ticket-1", "claude-code")
	tr.State = taskrun.StateRunning
	// Set created time well before now so we're past the grace period.
	tr.CreatedAt = time.Now().Add(-10 * time.Minute)
	return tr
}

func TestCheck_LoopDetection(t *testing.T) {
	tests := []struct {
		name           string
		consecutive    int
		filesChanged   int
		prevFiles      int
		wantReason     bool
		wantReasonCode string
	}{
		{
			name:        "below threshold no detection",
			consecutive: 5,
		},
		{
			name:           "at threshold with no file progress",
			consecutive:    10,
			filesChanged:   0,
			prevFiles:      0,
			wantReason:     true,
			wantReasonCode: "loop",
		},
		{
			name:         "at threshold but files changed",
			consecutive:  10,
			filesChanged: 3,
			prevFiles:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			w := New(cfg, testLogger())
			tr := runningTaskRun()

			current := &Heartbeat{
				Seq:                       2,
				RunID:                     tr.ID,
				Timestamp:                 time.Now(),
				TokensConsumed:            5000,
				FilesChanged:              tt.filesChanged,
				ToolCallsTotal:            20,
				LastToolName:              "edit_file",
				ConsecutiveIdenticalCalls: tt.consecutive,
				CostEstimateUSD:           0.50,
			}
			previous := &Heartbeat{
				Seq:            1,
				RunID:          tr.ID,
				Timestamp:      time.Now().Add(-1 * time.Minute),
				FilesChanged:   tt.prevFiles,
				ToolCallsTotal: 10,
			}

			// First tick — should not trigger (below consecutive ticks threshold).
			reason, _, err := w.Check(tr, current, previous)
			require.NoError(t, err)
			if !tt.wantReason {
				assert.Nil(t, reason)
				return
			}
			assert.Nil(t, reason, "first tick should not trigger action")

			// Second tick — should trigger.
			reason, action, err := w.Check(tr, current, previous)
			require.NoError(t, err)
			require.NotNil(t, reason)
			assert.Equal(t, tt.wantReasonCode, reason.ReasonCode)
			assert.Equal(t, ActionTerminateWithFeedback, action)
		})
	}
}

func TestCheck_ThrashingDetection(t *testing.T) {
	cfg := DefaultConfig()
	w := New(cfg, testLogger())
	tr := runningTaskRun()

	previous := &Heartbeat{
		Seq:            1,
		RunID:          tr.ID,
		Timestamp:      time.Now().Add(-1 * time.Minute),
		TokensConsumed: 0,
		FilesChanged:   0,
		ToolCallsTotal: 5,
	}

	current := &Heartbeat{
		Seq:             2,
		RunID:           tr.ID,
		Timestamp:       time.Now(),
		TokensConsumed:  90000, // exceeds 80000 threshold
		FilesChanged:    0,     // no progress
		ToolCallsTotal:  50,
		CostEstimateUSD: 1.20,
	}

	// First tick — below consecutive threshold.
	reason, _, err := w.Check(tr, current, previous)
	require.NoError(t, err)
	assert.Nil(t, reason)

	// Second tick — should trigger warn (first escalation level).
	reason, action, err := w.Check(tr, current, previous)
	require.NoError(t, err)
	require.NotNil(t, reason)
	assert.Equal(t, "thrashing", reason.ReasonCode)
	assert.Equal(t, ActionWarn, action)
}

func TestCheck_StallDetection(t *testing.T) {
	cfg := DefaultConfig()
	w := New(cfg, testLogger())
	tr := runningTaskRun()

	now := time.Now()
	previous := &Heartbeat{
		Seq:            1,
		RunID:          tr.ID,
		Timestamp:      now.Add(-6 * time.Minute), // 6 minutes ago
		ToolCallsTotal: 10,
	}

	current := &Heartbeat{
		Seq:            2,
		RunID:          tr.ID,
		Timestamp:      now,
		ToolCallsTotal: 10, // no new tool calls
	}

	// First tick.
	reason, _, err := w.Check(tr, current, previous)
	require.NoError(t, err)
	assert.Nil(t, reason)

	// Second tick — should trigger.
	reason, action, err := w.Check(tr, current, previous)
	require.NoError(t, err)
	require.NotNil(t, reason)
	assert.Equal(t, "stall", reason.ReasonCode)
	assert.Equal(t, ActionTerminate, action)
}

func TestCheck_CostVelocity(t *testing.T) {
	cfg := DefaultConfig()
	w := New(cfg, testLogger())
	tr := runningTaskRun()

	now := time.Now()
	previous := &Heartbeat{
		Seq:             1,
		RunID:           tr.ID,
		Timestamp:       now.Add(-1 * time.Minute),
		TokensConsumed:  10000,
		ToolCallsTotal:  10,
		FilesChanged:    5, // has file progress, so thrashing won't trigger
		CostEstimateUSD: 0,
	}

	current := &Heartbeat{
		Seq:             2,
		RunID:           tr.ID,
		Timestamp:       now,
		TokensConsumed:  20000,
		ToolCallsTotal:  20,
		FilesChanged:    10,
		CostEstimateUSD: 5.0, // $5 in 1 minute = $50/10min, exceeds $15/10min
	}

	// First tick.
	reason, _, err := w.Check(tr, current, previous)
	require.NoError(t, err)
	assert.Nil(t, reason)

	// Second tick — should trigger.
	reason, action, err := w.Check(tr, current, previous)
	require.NoError(t, err)
	require.NotNil(t, reason)
	assert.Equal(t, "cost_velocity", reason.ReasonCode)
	assert.Equal(t, ActionWarn, action)
}

func TestCheck_GracePeriod(t *testing.T) {
	cfg := DefaultConfig()
	w := New(cfg, testLogger())

	// TaskRun created very recently — within grace period.
	tr := taskrun.New("tr-grace", "idem-1", "ticket-1", "claude-code")
	tr.State = taskrun.StateRunning

	previous := &Heartbeat{
		Seq:            1,
		RunID:          tr.ID,
		Timestamp:      time.Now().Add(-1 * time.Minute),
		TokensConsumed: 0,
		FilesChanged:   0,
		ToolCallsTotal: 5,
	}

	current := &Heartbeat{
		Seq:             2,
		RunID:           tr.ID,
		Timestamp:       time.Now(),
		TokensConsumed:  90000, // would trigger thrashing normally
		FilesChanged:    0,
		ToolCallsTotal:  50,
		CostEstimateUSD: 1.00,
	}

	// Should not trigger thrashing during grace period.
	reason, _, err := w.Check(tr, current, previous)
	require.NoError(t, err)
	assert.Nil(t, reason)
}

func TestCheck_ConsecutiveTickRequirement(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinConsecutiveTicks = 3
	w := New(cfg, testLogger())
	tr := runningTaskRun()

	current := &Heartbeat{
		Seq:                       2,
		RunID:                     tr.ID,
		Timestamp:                 time.Now(),
		TokensConsumed:            5000,
		ConsecutiveIdenticalCalls: 15, // above loop threshold
		CostEstimateUSD:           0.50,
	}
	previous := &Heartbeat{
		Seq:            1,
		RunID:          tr.ID,
		Timestamp:      time.Now().Add(-1 * time.Minute),
		ToolCallsTotal: 10,
	}

	// Ticks 1 and 2 should not trigger.
	for i := 0; i < 2; i++ {
		reason, _, err := w.Check(tr, current, previous)
		require.NoError(t, err)
		assert.Nil(t, reason, "tick %d should not trigger", i+1)
	}

	// Tick 3 should trigger.
	reason, _, err := w.Check(tr, current, previous)
	require.NoError(t, err)
	require.NotNil(t, reason)
	assert.Equal(t, "loop", reason.ReasonCode)
}

func TestCheck_NeedsHumanState(t *testing.T) {
	t.Run("unanswered human triggers after timeout", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Rules.UnansweredHumanTimeoutMinutes = 1
		cfg.MinConsecutiveTicks = 1 // make it trigger on first tick for test
		w := New(cfg, testLogger())

		tr := taskrun.New("tr-human", "idem-1", "ticket-1", "claude-code")
		tr.State = taskrun.StateNeedsHuman
		tr.UpdatedAt = time.Now().Add(-2 * time.Minute) // 2 minutes ago, exceeds 1 minute timeout

		current := &Heartbeat{
			Seq:   1,
			RunID: tr.ID,
		}

		reason, action, err := w.Check(tr, current, nil)
		require.NoError(t, err)
		require.NotNil(t, reason)
		assert.Equal(t, "unanswered_human", reason.ReasonCode)
		assert.Equal(t, ActionTerminateAndNotify, action)
	})

	t.Run("running rules inactive in NeedsHuman state", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.MinConsecutiveTicks = 1
		cfg.Rules.UnansweredHumanTimeoutMinutes = 60 // long timeout
		w := New(cfg, testLogger())

		tr := taskrun.New("tr-human-2", "idem-1", "ticket-1", "claude-code")
		tr.State = taskrun.StateNeedsHuman

		current := &Heartbeat{
			Seq:                       2,
			RunID:                     tr.ID,
			Timestamp:                 time.Now(),
			TokensConsumed:            100000,
			ConsecutiveIdenticalCalls: 50,
			CostEstimateUSD:           10.0,
		}

		// Running rules (loop, thrashing, stall, cost, telemetry) should not trigger.
		reason, _, err := w.Check(tr, current, nil)
		require.NoError(t, err)
		assert.Nil(t, reason)
	})
}

func TestCheck_NilInputs(t *testing.T) {
	cfg := DefaultConfig()
	w := New(cfg, testLogger())

	reason, _, err := w.Check(nil, &Heartbeat{}, nil)
	require.NoError(t, err)
	assert.Nil(t, reason)

	reason, _, err = w.Check(runningTaskRun(), nil, nil)
	require.NoError(t, err)
	assert.Nil(t, reason)
}

func TestCheck_TelemetryFailure(t *testing.T) {
	cfg := DefaultConfig()
	w := New(cfg, testLogger())
	tr := runningTaskRun()

	previous := &Heartbeat{
		Seq:            5,
		RunID:          tr.ID,
		Timestamp:      time.Now().Add(-1 * time.Minute),
		ToolCallsTotal: 20,
		FilesChanged:   3,
	}

	// Seq has NOT advanced.
	current := &Heartbeat{
		Seq:            5,
		RunID:          tr.ID,
		Timestamp:      time.Now(),
		ToolCallsTotal: 20,
		FilesChanged:   3,
	}

	// First tick.
	reason, _, err := w.Check(tr, current, previous)
	require.NoError(t, err)
	assert.Nil(t, reason)

	// Second tick — should trigger.
	reason, action, err := w.Check(tr, current, previous)
	require.NoError(t, err)
	require.NotNil(t, reason)
	assert.Equal(t, "telemetry_failure", reason.ReasonCode)
	assert.Equal(t, ActionWarn, action)
}

func TestStart_CancelsOnContext(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CheckIntervalSeconds = 1 // fast interval for test
	w := New(cfg, testLogger())

	ctx, cancel := context.WithCancel(context.Background())

	callCount := 0
	done := make(chan struct{})
	go func() {
		w.Start(ctx, func(_ context.Context) {
			callCount++
			if callCount >= 2 {
				cancel()
			}
		})
		close(done)
	}()

	select {
	case <-done:
		assert.GreaterOrEqual(t, callCount, 2)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("watchdog did not stop within timeout")
	}
}
