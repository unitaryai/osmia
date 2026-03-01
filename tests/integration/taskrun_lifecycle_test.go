//go:build integration

// Package integration_test contains Tier 2 integration tests that exercise
// the TaskRun state machine, controller guardrails, and secret resolver
// without requiring a live Kubernetes cluster.
package integration_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/robodev/internal/taskrun"
)

// TestTaskRunHappyPath verifies the canonical Queued→Running→Succeeded path.
func TestTaskRunHappyPath(t *testing.T) {
	tr := taskrun.New("tr-1", "idem-1", "TICKET-1", "test-engine")

	require.Equal(t, taskrun.StateQueued, tr.State)
	assert.False(t, tr.IsTerminal())

	require.NoError(t, tr.Transition(taskrun.StateRunning))
	assert.Equal(t, taskrun.StateRunning, tr.State)
	assert.False(t, tr.IsTerminal())

	require.NoError(t, tr.Transition(taskrun.StateSucceeded))
	assert.Equal(t, taskrun.StateSucceeded, tr.State)
	assert.True(t, tr.IsTerminal())
}

// TestTaskRunFailureAndRetry verifies that a failed run retries and ultimately
// succeeds, and that RetryCount is incremented correctly on the Failed transition.
func TestTaskRunFailureAndRetry(t *testing.T) {
	tr := taskrun.New("tr-2", "idem-2", "TICKET-2", "test-engine")
	// Default MaxRetries is 1.
	require.Equal(t, 1, tr.MaxRetries)

	require.NoError(t, tr.Transition(taskrun.StateRunning))

	// Transition to Failed — with RetryCount=0 this is not yet terminal.
	require.NoError(t, tr.Transition(taskrun.StateFailed))
	assert.Equal(t, taskrun.StateFailed, tr.State)
	assert.False(t, tr.IsTerminal(), "failed with retries remaining should not be terminal")

	// The controller increments RetryCount after each Failed→Retrying transition.
	tr.RetryCount++
	assert.Equal(t, 1, tr.RetryCount)

	require.NoError(t, tr.Transition(taskrun.StateRetrying))
	assert.Equal(t, taskrun.StateRetrying, tr.State)

	require.NoError(t, tr.Transition(taskrun.StateRunning))
	assert.Equal(t, taskrun.StateRunning, tr.State)

	require.NoError(t, tr.Transition(taskrun.StateSucceeded))
	assert.Equal(t, taskrun.StateSucceeded, tr.State)
	assert.True(t, tr.IsTerminal())
}

// TestTaskRunTimeout verifies the Running→TimedOut path and that TimedOut is terminal.
func TestTaskRunTimeout(t *testing.T) {
	tr := taskrun.New("tr-3", "idem-3", "TICKET-3", "test-engine")

	require.NoError(t, tr.Transition(taskrun.StateRunning))
	require.NoError(t, tr.Transition(taskrun.StateTimedOut))

	assert.Equal(t, taskrun.StateTimedOut, tr.State)
	assert.True(t, tr.IsTerminal())
}

// TestTaskRunNeedsHuman verifies that NeedsHuman can yield back to Running,
// allowing the agent to resume after human intervention.
func TestTaskRunNeedsHuman(t *testing.T) {
	tr := taskrun.New("tr-4", "idem-4", "TICKET-4", "test-engine")

	require.NoError(t, tr.Transition(taskrun.StateRunning))
	require.NoError(t, tr.Transition(taskrun.StateNeedsHuman))
	assert.Equal(t, taskrun.StateNeedsHuman, tr.State)
	assert.False(t, tr.IsTerminal())

	// Resume after human responds.
	require.NoError(t, tr.Transition(taskrun.StateRunning))
	require.NoError(t, tr.Transition(taskrun.StateSucceeded))
	assert.True(t, tr.IsTerminal())
}

// TestTaskRunInvalidTransitions verifies that every disallowed state transition
// returns an error, keeping the state machine in a consistent state.
func TestTaskRunInvalidTransitions(t *testing.T) {
	tests := []struct {
		name string
		from taskrun.State
		to   taskrun.State
	}{
		{"Queued→Succeeded", taskrun.StateQueued, taskrun.StateSucceeded},
		{"Queued→Failed", taskrun.StateQueued, taskrun.StateFailed},
		{"Queued→TimedOut", taskrun.StateQueued, taskrun.StateTimedOut},
		// Note: Queued→NeedsHuman is valid (approval gates), so not listed here.
		{"Running→Queued", taskrun.StateRunning, taskrun.StateQueued},
		{"Running→Retrying", taskrun.StateRunning, taskrun.StateRetrying},
		// Failed must go through Retrying before Running again.
		{"Failed→Running", taskrun.StateFailed, taskrun.StateRunning},
		{"Retrying→Succeeded", taskrun.StateRetrying, taskrun.StateSucceeded},
		{"Retrying→Failed", taskrun.StateRetrying, taskrun.StateFailed},
		// Terminal states have no outgoing transitions.
		{"Succeeded→Running", taskrun.StateSucceeded, taskrun.StateRunning},
		{"TimedOut→Running", taskrun.StateTimedOut, taskrun.StateRunning},
		{"NeedsHuman→Succeeded", taskrun.StateNeedsHuman, taskrun.StateSucceeded},
		{"NeedsHuman→Failed", taskrun.StateNeedsHuman, taskrun.StateFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := taskrun.New("tr-inv", "idem-inv", "TICKET-INV", "test-engine")
			// Drive the TaskRun into the required starting state.
			setStateForTest(t, tr, tt.from)

			err := tr.Transition(tt.to)
			assert.Error(t, err, "expected error for transition %s → %s", tt.from, tt.to)
			// State must not have changed on an invalid transition.
			assert.Equal(t, tt.from, tr.State)
		})
	}
}

// setStateForTest drives a TaskRun into the desired state by performing the
// minimum valid transitions required to reach it.
func setStateForTest(t *testing.T, tr *taskrun.TaskRun, target taskrun.State) {
	t.Helper()
	switch target {
	case taskrun.StateQueued:
		// Already in Queued after New().
	case taskrun.StateRunning:
		require.NoError(t, tr.Transition(taskrun.StateRunning))
	case taskrun.StateNeedsHuman:
		require.NoError(t, tr.Transition(taskrun.StateRunning))
		require.NoError(t, tr.Transition(taskrun.StateNeedsHuman))
	case taskrun.StateSucceeded:
		require.NoError(t, tr.Transition(taskrun.StateRunning))
		require.NoError(t, tr.Transition(taskrun.StateSucceeded))
	case taskrun.StateFailed:
		require.NoError(t, tr.Transition(taskrun.StateRunning))
		require.NoError(t, tr.Transition(taskrun.StateFailed))
	case taskrun.StateTimedOut:
		require.NoError(t, tr.Transition(taskrun.StateRunning))
		require.NoError(t, tr.Transition(taskrun.StateTimedOut))
	case taskrun.StateRetrying:
		require.NoError(t, tr.Transition(taskrun.StateRunning))
		require.NoError(t, tr.Transition(taskrun.StateFailed))
		require.NoError(t, tr.Transition(taskrun.StateRetrying))
	default:
		t.Fatalf("setStateForTest: unknown target state %q", target)
	}
}
