package prm

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/llm"
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

func TestLLMScorer_UsesLLMPath(t *testing.T) {
	mock := &mockModule{
		outputs: map[string]any{
			"score":     8,
			"reasoning": "good tool usage pattern",
		},
	}
	logger := slog.Default()
	fallback := NewScorer(logger, 10)
	scorer := newLLMScorerWithModule(mock, fallback, logger)

	events := []*agentstream.StreamEvent{
		{
			Type:   agentstream.EventToolCall,
			Parsed: &agentstream.ToolCallEvent{Tool: "Read", Args: []byte(`{"path":"main.go"}`)},
		},
	}

	result := scorer.ScoreStep(events)
	require.NotNil(t, result)
	assert.Equal(t, 8, result.Score)
	assert.Equal(t, "good tool usage pattern", result.Reasoning)
}

func TestLLMScorer_FallsBackOnError(t *testing.T) {
	mock := &mockModule{
		err: fmt.Errorf("api unavailable"),
	}
	logger := slog.Default()
	fallback := NewScorer(logger, 10)
	scorer := newLLMScorerWithModule(mock, fallback, logger)

	events := []*agentstream.StreamEvent{
		{
			Type:   agentstream.EventToolCall,
			Parsed: &agentstream.ToolCallEvent{Tool: "Read"},
		},
	}

	result := scorer.ScoreStep(events)
	require.NotNil(t, result)
	assert.GreaterOrEqual(t, result.Score, 1)
	assert.LessOrEqual(t, result.Score, 10)
}

func TestLLMScorer_FallsBackOnInvalidScore(t *testing.T) {
	mock := &mockModule{
		outputs: map[string]any{
			"score":     99,
			"reasoning": "invalid score",
		},
	}
	logger := slog.Default()
	fallback := NewScorer(logger, 10)
	scorer := newLLMScorerWithModule(mock, fallback, logger)

	events := []*agentstream.StreamEvent{
		{
			Type:   agentstream.EventToolCall,
			Parsed: &agentstream.ToolCallEvent{Tool: "Read"},
		},
	}

	result := scorer.ScoreStep(events)
	require.NotNil(t, result)
	// Should be a valid fallback score in [1,10], not 99.
	assert.GreaterOrEqual(t, result.Score, 1)
	assert.LessOrEqual(t, result.Score, 10)
	assert.NotEqual(t, 99, result.Score)
}
