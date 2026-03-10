package memory

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/llm"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/pkg/engine"
)

// mockModule implements llm.Module for testing.
type mockModule struct {
	outputs map[string]any
	err     error
}

func (m *mockModule) Forward(_ context.Context, _ map[string]any) (map[string]any, error) {
	return m.outputs, m.err
}

func (m *mockModule) GetSignature() llm.Signature { return llm.Signature{} }

func TestLLMExtractor_MergesWithV1(t *testing.T) {
	mock := &mockModule{
		outputs: map[string]any{
			"facts":    `["discovered caching pattern", "API rate limit encountered"]`,
			"patterns": `["repeated retry behaviour"]`,
		},
	}
	logger := slog.Default()
	v1 := NewExtractor(logger)
	extractor := newLLMExtractorWithModule(mock, v1, logger)

	tr := taskrun.New("test-task-1", "idem-1", "TICKET-123", "claude-code")
	_ = tr.Transition(taskrun.StateRunning)
	_ = tr.Transition(taskrun.StateSucceeded)
	tr.Result = &engine.TaskResult{Success: true, Summary: "all tests passed"}

	events := []agentstream.StreamEvent{}

	nodes, edges, err := extractor.Extract(context.Background(), tr, events)
	require.NoError(t, err)

	// V1 should produce at least a success-pattern node; LLM adds 2 facts + 1 pattern.
	// Get V1-only count for comparison.
	v1Nodes, _, _ := v1.Extract(context.Background(), tr, events)
	assert.Greater(t, len(nodes), len(v1Nodes),
		"LLM extractor should produce more nodes than V1 alone")
	_ = edges
}

func TestLLMExtractor_FallsBackOnError(t *testing.T) {
	mock := &mockModule{
		err: fmt.Errorf("llm unavailable"),
	}
	logger := slog.Default()
	v1 := NewExtractor(logger)
	extractor := newLLMExtractorWithModule(mock, v1, logger)

	tr := taskrun.New("test-task-2", "idem-2", "TICKET-456", "claude-code")
	_ = tr.Transition(taskrun.StateRunning)
	_ = tr.Transition(taskrun.StateFailed)
	tr.Result = &engine.TaskResult{Success: false, Summary: "compilation error"}

	events := []agentstream.StreamEvent{}

	nodes, _, err := extractor.Extract(context.Background(), tr, events)
	require.NoError(t, err)

	// Should still return V1 results.
	v1Nodes, _, _ := v1.Extract(context.Background(), tr, events)
	assert.Equal(t, len(v1Nodes), len(nodes),
		"on LLM error, should return exactly the V1 results")
}
