package watchdog

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/taskrun"
)

func TestConsumeStreamEvent(t *testing.T) {
	tests := []struct {
		name    string
		event   *agentstream.StreamEvent
		initial *taskrun.TaskRun
		assert  func(t *testing.T, tr *taskrun.TaskRun)
	}{
		{
			name: "tool call updates tracking fields",
			event: &agentstream.StreamEvent{
				Type: agentstream.EventToolCall,
				Parsed: &agentstream.ToolCallEvent{
					Tool: "Bash",
				},
			},
			initial: func() *taskrun.TaskRun {
				tr := taskrun.New("tr-1", "idem-1", "ticket-1", "claude-code")
				tr.ToolCallsTotal = 5
				tr.LastToolName = "Read"
				tr.ConsecutiveIdenticalTools = 2
				return tr
			}(),
			assert: func(t *testing.T, tr *taskrun.TaskRun) {
				assert.Equal(t, 6, tr.ToolCallsTotal)
				assert.Equal(t, "Bash", tr.LastToolName)
				assert.Equal(t, 1, tr.ConsecutiveIdenticalTools, "different tool should reset consecutive count")
			},
		},
		{
			name: "consecutive identical tool calls increment counter",
			event: &agentstream.StreamEvent{
				Type: agentstream.EventToolCall,
				Parsed: &agentstream.ToolCallEvent{
					Tool: "Bash",
				},
			},
			initial: func() *taskrun.TaskRun {
				tr := taskrun.New("tr-2", "idem-2", "ticket-2", "claude-code")
				tr.ToolCallsTotal = 10
				tr.LastToolName = "Bash"
				tr.ConsecutiveIdenticalTools = 3
				return tr
			}(),
			assert: func(t *testing.T, tr *taskrun.TaskRun) {
				assert.Equal(t, 11, tr.ToolCallsTotal)
				assert.Equal(t, "Bash", tr.LastToolName)
				assert.Equal(t, 4, tr.ConsecutiveIdenticalTools)
			},
		},
		{
			name: "cost event updates token consumption",
			event: &agentstream.StreamEvent{
				Type: agentstream.EventCost,
				Parsed: &agentstream.CostEvent{
					InputTokens:  15000,
					OutputTokens: 5000,
					CostUSD:      0.75,
				},
			},
			initial: func() *taskrun.TaskRun {
				tr := taskrun.New("tr-3", "idem-3", "ticket-3", "claude-code")
				tr.TokensConsumed = 1000
				return tr
			}(),
			assert: func(t *testing.T, tr *taskrun.TaskRun) {
				assert.Equal(t, 20000, tr.TokensConsumed, "should be sum of input + output tokens")
			},
		},
		{
			name: "content delta updates heartbeat timestamp",
			event: &agentstream.StreamEvent{
				Type: agentstream.EventContentDelta,
				Parsed: &agentstream.ContentDeltaEvent{
					Content: "some output",
					Role:    "assistant",
				},
			},
			initial: func() *taskrun.TaskRun {
				tr := taskrun.New("tr-4", "idem-4", "ticket-4", "claude-code")
				// HeartbeatAt starts nil.
				return tr
			}(),
			assert: func(t *testing.T, tr *taskrun.TaskRun) {
				require.NotNil(t, tr.HeartbeatAt, "heartbeat should be set after content delta")
				assert.WithinDuration(t, time.Now(), *tr.HeartbeatAt, 2*time.Second)
			},
		},
		{
			name: "result event handled without error",
			event: &agentstream.StreamEvent{
				Type: agentstream.EventResult,
				Parsed: &agentstream.ResultEvent{
					Success: true,
					Summary: "all tests pass",
				},
			},
			initial: func() *taskrun.TaskRun {
				tr := taskrun.New("tr-5", "idem-5", "ticket-5", "claude-code")
				return tr
			}(),
			assert: func(t *testing.T, tr *taskrun.TaskRun) {
				// Result events are logged but do not mutate the TaskRun directly.
				// Verify no panic or unexpected state change.
				assert.Equal(t, 0, tr.ToolCallsTotal)
				assert.Equal(t, 0, tr.TokensConsumed)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			w := New(cfg, testLogger())

			w.ConsumeStreamEvent(tt.initial, tt.event)
			tt.assert(t, tt.initial)
		})
	}
}

func TestConsumeStreamEvent_NilInputs(t *testing.T) {
	cfg := DefaultConfig()
	w := New(cfg, testLogger())

	// Nil task run should not panic.
	w.ConsumeStreamEvent(nil, &agentstream.StreamEvent{
		Type:   agentstream.EventToolCall,
		Parsed: &agentstream.ToolCallEvent{Tool: "Bash"},
	})

	// Nil event should not panic.
	tr := taskrun.New("tr-nil", "idem-nil", "ticket-nil", "claude-code")
	w.ConsumeStreamEvent(tr, nil)

	// Unknown/nil parsed event should not panic.
	w.ConsumeStreamEvent(tr, &agentstream.StreamEvent{
		Type:   agentstream.EventSystem,
		Parsed: nil,
	})
}
