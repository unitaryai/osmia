package diagnosis

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestLLMAnalyser_UsesLLMPath(t *testing.T) {
	mock := &mockModule{
		outputs: map[string]any{
			"failure_mode": "wrong_approach",
			"confidence":   0.8,
			"evidence":     `["edited wrong files", "changes unrelated to task"]`,
		},
	}
	logger := slog.Default()
	fallback := NewAnalyser(logger)
	analyser := newLLMAnalyserWithModule(mock, fallback, logger)

	tr := taskrun.New("test-task-1", "idem-1", "TICKET-123", "claude-code")
	_ = tr.Transition(taskrun.StateRunning)
	_ = tr.Transition(taskrun.StateFailed)

	input := DiagnosisInput{
		TaskRun:        tr,
		WatchdogReason: "stall detected",
		Result:         &engine.TaskResult{Success: false, Summary: "wrong files edited"},
	}

	diagnosis, err := analyser.Analyse(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, diagnosis)
	assert.Equal(t, WrongApproach, diagnosis.Mode)
	assert.InDelta(t, 0.8, diagnosis.Confidence, 0.001)
}

func TestLLMAnalyser_FallsBackOnUnknownMode(t *testing.T) {
	mock := &mockModule{
		outputs: map[string]any{
			"failure_mode": "nonexistent_mode",
			"confidence":   0.9,
			"evidence":     `[]`,
		},
	}
	logger := slog.Default()
	fallback := NewAnalyser(logger)
	analyser := newLLMAnalyserWithModule(mock, fallback, logger)

	tr := taskrun.New("test-task-2", "idem-2", "TICKET-456", "claude-code")
	_ = tr.Transition(taskrun.StateRunning)
	_ = tr.Transition(taskrun.StateFailed)

	input := DiagnosisInput{
		TaskRun: tr,
		Result:  &engine.TaskResult{Success: false, Summary: "something failed"},
	}

	diagnosis, err := analyser.Analyse(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, diagnosis)
	// Should be a valid failure mode from the fallback.
	validMode := false
	for _, m := range AllFailureModes {
		if m == diagnosis.Mode {
			validMode = true
			break
		}
	}
	assert.True(t, validMode, "expected a valid failure mode from fallback, got %q", diagnosis.Mode)
}

func TestLLMAnalyser_FallsBackOnError(t *testing.T) {
	mock := &mockModule{
		err: fmt.Errorf("api unavailable"),
	}
	logger := slog.Default()
	fallback := NewAnalyser(logger)
	analyser := newLLMAnalyserWithModule(mock, fallback, logger)

	tr := taskrun.New("test-task-3", "idem-3", "TICKET-789", "claude-code")
	_ = tr.Transition(taskrun.StateRunning)
	_ = tr.Transition(taskrun.StateFailed)

	input := DiagnosisInput{
		TaskRun:        tr,
		WatchdogReason: "timeout",
		Result:         &engine.TaskResult{Success: false, Summary: "OOMKilled"},
	}

	diagnosis, err := analyser.Analyse(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, diagnosis)
	// Fallback should return a valid result.
	validMode := false
	for _, m := range AllFailureModes {
		if m == diagnosis.Mode {
			validMode = true
			break
		}
	}
	assert.True(t, validMode, "expected a valid failure mode from fallback, got %q", diagnosis.Mode)
}
