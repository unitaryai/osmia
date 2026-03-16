package agentstream

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForwarder_Forward(t *testing.T) {
	tests := []struct {
		name          string
		events        []*StreamEvent
		useWatchdog   bool
		wantWatchdogN int
	}{
		{
			name: "events forwarded to watchdog channel",
			events: []*StreamEvent{
				{Type: EventToolCall, Parsed: &ToolCallEvent{Tool: "Bash"}},
				{Type: EventCost, Parsed: &CostEvent{InputTokens: 100, OutputTokens: 50, CostUSD: 0.01}},
				{Type: EventResult, Parsed: &ResultEvent{Success: true, Summary: "done"}},
			},
			useWatchdog:   true,
			wantWatchdogN: 3,
		},
		{
			name: "events processed without watchdog channel",
			events: []*StreamEvent{
				{Type: EventResult, Parsed: &ResultEvent{Success: true, Summary: "done"}},
			},
			useWatchdog:   false,
			wantWatchdogN: 0,
		},
		{
			name:          "empty event stream",
			events:        nil,
			useWatchdog:   true,
			wantWatchdogN: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))

			var opts []ForwarderOption
			watchdogCh := make(chan *StreamEvent, 100)
			if tt.useWatchdog {
				opts = append(opts, WithWatchdogChannel(watchdogCh))
			}

			fwd := NewForwarder(logger, opts...)

			eventCh := make(chan *StreamEvent, len(tt.events))
			for _, ev := range tt.events {
				eventCh <- ev
			}
			close(eventCh)

			err := fwd.Forward(context.Background(), eventCh)
			require.NoError(t, err)

			close(watchdogCh)
			var received []*StreamEvent
			for ev := range watchdogCh {
				received = append(received, ev)
			}

			assert.Len(t, received, tt.wantWatchdogN)
		})
	}
}

func TestForwarder_ContextCancellation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fwd := NewForwarder(logger)

	eventCh := make(chan *StreamEvent) // unbuffered, will block
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- fwd.Forward(ctx, eventCh)
	}()

	cancel()

	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Forward to return")
	}
}

func TestWithNotifiers(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fwd := NewForwarder(logger, WithNotifiers(nil))
	assert.Nil(t, fwd.notifiers)
}

func TestNewForwarder_Defaults(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fwd := NewForwarder(logger)

	assert.Nil(t, fwd.watchdogCh)
	assert.Nil(t, fwd.notifiers)
	assert.NotNil(t, fwd.logger)
}
