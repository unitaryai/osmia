package noop

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/pkg/engine"
)

func TestPollReadyTickets_ReturnsEmpty(t *testing.T) {
	backend := New()

	tickets, err := backend.PollReadyTickets(context.Background())
	require.NoError(t, err)
	assert.Empty(t, tickets)
}

func TestLifecycleMethods_AreNoOps(t *testing.T) {
	backend := New()
	ctx := context.Background()

	t.Run("MarkInProgress", func(t *testing.T) {
		require.NoError(t, backend.MarkInProgress(ctx, "TICKET-1"))
	})
	t.Run("MarkComplete", func(t *testing.T) {
		require.NoError(t, backend.MarkComplete(ctx, "TICKET-1", engine.TaskResult{Summary: "done"}))
	})
	t.Run("MarkFailed", func(t *testing.T) {
		require.NoError(t, backend.MarkFailed(ctx, "TICKET-1", "boom"))
	})
	t.Run("AddComment", func(t *testing.T) {
		require.NoError(t, backend.AddComment(ctx, "TICKET-1", "comment"))
	})
}

func TestName(t *testing.T) {
	backend := New()
	assert.Equal(t, "noop", backend.Name())
}

func TestInterfaceVersion(t *testing.T) {
	backend := New()
	assert.Equal(t, 1, backend.InterfaceVersion())
}
