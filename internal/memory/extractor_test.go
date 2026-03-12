package memory

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/pkg/engine"
)

func TestExtractor_Extract(t *testing.T) {
	t.Parallel()

	now := time.Now()
	heartbeat := now.Add(-10 * time.Minute)

	tests := []struct {
		name          string
		taskRun       *taskrun.TaskRun
		events        []agentstream.StreamEvent
		wantNodes     int
		wantEdges     int
		wantErr       string
		checkNodeType NodeType
		checkFactKind FactType
	}{
		{
			name: "success extracts success pattern",
			taskRun: func() *taskrun.TaskRun {
				tr := taskrun.New("tr-1", "idem-1", "ticket-1", "claude-code")
				tr.State = taskrun.StateSucceeded
				tr.CurrentEngine = "claude-code"
				tr.Result = &engine.TaskResult{Success: true, Summary: "fixed the bug"}
				return tr
			}(),
			events:        nil,
			wantNodes:     1,
			wantEdges:     0,
			checkNodeType: NodeTypeFact,
			checkFactKind: FactTypeSuccessPattern,
		},
		{
			name: "failure extracts failure pattern",
			taskRun: func() *taskrun.TaskRun {
				tr := taskrun.New("tr-2", "idem-2", "ticket-2", "codex")
				tr.State = taskrun.StateFailed
				tr.CurrentEngine = "codex"
				tr.Result = &engine.TaskResult{Success: false, Summary: "compilation error"}
				tr.RetryCount = 1
				tr.MaxRetries = 1
				return tr
			}(),
			events:        nil,
			wantNodes:     1,
			wantEdges:     0,
			checkNodeType: NodeTypeFact,
			checkFactKind: FactTypeFailurePattern,
		},
		{
			name: "timeout extracts failure pattern",
			taskRun: func() *taskrun.TaskRun {
				tr := taskrun.New("tr-3", "idem-3", "ticket-3", "claude-code")
				tr.State = taskrun.StateTimedOut
				tr.CurrentEngine = "claude-code"
				return tr
			}(),
			events:        nil,
			wantNodes:     1,
			wantEdges:     0,
			checkNodeType: NodeTypeFact,
			checkFactKind: FactTypeFailurePattern,
		},
		{
			name: "stale heartbeat extracts anomaly fact",
			taskRun: func() *taskrun.TaskRun {
				tr := taskrun.New("tr-4", "idem-4", "ticket-4", "claude-code")
				tr.State = taskrun.StateSucceeded
				tr.CurrentEngine = "claude-code"
				tr.HeartbeatAt = &heartbeat
				tr.HeartbeatTTLSeconds = 60 // TTL of 60s, heartbeat 10m ago → stale
				return tr
			}(),
			events:    nil,
			wantNodes: 2, // success + stale
			wantEdges: 1,
		},
		{
			name: "engine fallback extracts capability fact",
			taskRun: func() *taskrun.TaskRun {
				tr := taskrun.New("tr-5", "idem-5", "ticket-5", "claude-code")
				tr.State = taskrun.StateSucceeded
				tr.CurrentEngine = "codex"
				tr.EngineAttempts = []string{"claude-code", "codex"}
				return tr
			}(),
			events:    nil,
			wantNodes: 2, // success + fallback
			wantEdges: 1,
		},
		{
			name: "heavy tool usage extracts pattern",
			taskRun: func() *taskrun.TaskRun {
				tr := taskrun.New("tr-6", "idem-6", "ticket-6", "claude-code")
				tr.State = taskrun.StateSucceeded
				tr.CurrentEngine = "claude-code"
				return tr
			}(),
			events: func() []agentstream.StreamEvent {
				var events []agentstream.StreamEvent
				for i := 0; i < 7; i++ {
					events = append(events, agentstream.StreamEvent{
						Type:   agentstream.EventToolCall,
						Parsed: &agentstream.ToolCallEvent{Tool: "Bash"},
					})
				}
				return events
			}(),
			wantNodes: 2, // success + tool pattern
			wantEdges: 1,
		},
		{
			name:      "nil task run returns error",
			taskRun:   nil,
			events:    nil,
			wantErr:   "cannot extract from nil task run",
			wantNodes: 0,
			wantEdges: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ext := NewExtractor(testLogger())
			nodes, edges, err := ext.Extract(context.Background(), tt.taskRun, tt.events)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Len(t, nodes, tt.wantNodes, "node count mismatch")
			assert.Len(t, edges, tt.wantEdges, "edge count mismatch")

			if tt.checkNodeType != "" && len(nodes) > 0 {
				assert.Equal(t, tt.checkNodeType, nodes[0].NodeType())
			}
			if tt.checkFactKind != "" && len(nodes) > 0 {
				fact, ok := nodes[0].(*Fact)
				require.True(t, ok, "first node should be a Fact")
				assert.Equal(t, tt.checkFactKind, fact.FactKind)
			}
		})
	}
}

func TestExtractor_CountToolCalls(t *testing.T) {
	t.Parallel()

	events := []agentstream.StreamEvent{
		{Type: agentstream.EventToolCall, Parsed: &agentstream.ToolCallEvent{Tool: "Bash"}},
		{Type: agentstream.EventToolCall, Parsed: &agentstream.ToolCallEvent{Tool: "Read"}},
		{Type: agentstream.EventToolCall, Parsed: &agentstream.ToolCallEvent{Tool: "Bash"}},
		{Type: agentstream.EventContentDelta, Parsed: &agentstream.ContentDeltaEvent{Content: "hello"}},
		{Type: agentstream.EventToolCall, Parsed: &agentstream.ToolCallEvent{Tool: "Bash"}},
		{Type: agentstream.EventToolCall, Parsed: &agentstream.ToolCallEvent{Tool: "Write"}},
	}

	counts := countToolCalls(events)
	assert.Equal(t, 3, counts["Bash"])
	assert.Equal(t, 1, counts["Read"])
	assert.Equal(t, 1, counts["Write"])
	assert.Equal(t, 0, counts["Edit"])
}
