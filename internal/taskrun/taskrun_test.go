package taskrun

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	before := time.Now()
	tr := New("id-1", "idem-key-1", "ticket-1", "claude-code")
	after := time.Now()

	assert.Equal(t, "id-1", tr.ID)
	assert.Equal(t, "idem-key-1", tr.IdempotencyKey)
	assert.Equal(t, "ticket-1", tr.TicketID)
	assert.Equal(t, "claude-code", tr.Engine)
	assert.Equal(t, StateQueued, tr.State)
	assert.Equal(t, 1, tr.MaxRetries)
	assert.Equal(t, 300, tr.HeartbeatTTLSeconds)
	assert.True(t, !tr.CreatedAt.Before(before) && !tr.CreatedAt.After(after))
	assert.Equal(t, tr.CreatedAt, tr.UpdatedAt)
}

func TestTransition(t *testing.T) {
	tests := []struct {
		name    string
		from    State
		to      State
		wantErr bool
	}{
		{name: "queued to running", from: StateQueued, to: StateRunning},
		{name: "running to succeeded", from: StateRunning, to: StateSucceeded},
		{name: "running to failed", from: StateRunning, to: StateFailed},
		{name: "running to needs human", from: StateRunning, to: StateNeedsHuman},
		{name: "running to timed out", from: StateRunning, to: StateTimedOut},
		{name: "needs human to running", from: StateNeedsHuman, to: StateRunning},
		{name: "failed to retrying", from: StateFailed, to: StateRetrying},
		{name: "retrying to running", from: StateRetrying, to: StateRunning},
		{name: "retrying to failed", from: StateRetrying, to: StateFailed},
		{name: "retrying to succeeded is invalid", from: StateRetrying, to: StateSucceeded, wantErr: true},
		{name: "needs human to failed", from: StateNeedsHuman, to: StateFailed},
		{name: "needs human to succeeded is invalid", from: StateNeedsHuman, to: StateSucceeded, wantErr: true},
		{name: "queued to succeeded is invalid", from: StateQueued, to: StateSucceeded, wantErr: true},
		{name: "queued to failed is invalid", from: StateQueued, to: StateFailed, wantErr: true},
		{name: "running to queued is invalid", from: StateRunning, to: StateQueued, wantErr: true},
		{name: "succeeded has no transitions", from: StateSucceeded, to: StateRunning, wantErr: true},
		{name: "timed out has no transitions", from: StateTimedOut, to: StateRunning, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := New("id-1", "idem-1", "ticket-1", "claude-code")
			tr.State = tt.from

			err := tr.Transition(tt.to)
			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.from, tr.State, "state should not change on invalid transition")
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.to, tr.State)
		})
	}
}

func TestTransition_UpdatesTimestamp(t *testing.T) {
	tr := New("id-1", "idem-1", "ticket-1", "claude-code")
	original := tr.UpdatedAt

	time.Sleep(time.Millisecond)
	err := tr.Transition(StateRunning)
	require.NoError(t, err)

	assert.True(t, tr.UpdatedAt.After(original), "UpdatedAt should advance after transition")
}

func TestIsTerminal(t *testing.T) {
	tests := []struct {
		name       string
		state      State
		retryCount int
		maxRetries int
		want       bool
	}{
		{name: "succeeded is terminal", state: StateSucceeded, want: true},
		{name: "timed out is terminal", state: StateTimedOut, want: true},
		{name: "failed with retries exhausted is terminal", state: StateFailed, retryCount: 1, maxRetries: 1, want: true},
		{name: "failed with retries remaining is not terminal", state: StateFailed, retryCount: 0, maxRetries: 1, want: false},
		{name: "queued is not terminal", state: StateQueued, want: false},
		{name: "running is not terminal", state: StateRunning, want: false},
		{name: "needs human is not terminal", state: StateNeedsHuman, want: false},
		{name: "retrying is not terminal", state: StateRetrying, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := New("id-1", "idem-1", "ticket-1", "claude-code")
			tr.State = tt.state
			tr.RetryCount = tt.retryCount
			tr.MaxRetries = tt.maxRetries

			assert.Equal(t, tt.want, tr.IsTerminal())
		})
	}
}

func TestIsStale(t *testing.T) {
	t.Run("no heartbeat is not stale", func(t *testing.T) {
		tr := New("id-1", "idem-1", "ticket-1", "claude-code")
		assert.False(t, tr.IsStale())
	})

	t.Run("recent heartbeat is not stale", func(t *testing.T) {
		tr := New("id-1", "idem-1", "ticket-1", "claude-code")
		now := time.Now()
		tr.HeartbeatAt = &now
		tr.HeartbeatTTLSeconds = 300
		assert.False(t, tr.IsStale())
	})

	t.Run("expired heartbeat is stale", func(t *testing.T) {
		tr := New("id-1", "idem-1", "ticket-1", "claude-code")
		past := time.Now().Add(-10 * time.Minute)
		tr.HeartbeatAt = &past
		tr.HeartbeatTTLSeconds = 300
		assert.True(t, tr.IsStale())
	})
}
