package agentstream

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nopCloser wraps an io.Reader to satisfy io.ReadCloser.
type nopCloser struct {
	io.Reader
}

func (nopCloser) Close() error { return nil }

func TestReadStream(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantEvents    int
		wantFirstType EventType
		wantLastType  EventType
		cancelAfterN  int // cancel context after N events; 0 means don't cancel
	}{
		{
			name: "multiple valid lines",
			input: strings.Join([]string{
				`{"type":"tool_use","tool":"Bash","args":{"command":"ls"},"timestamp":"2026-01-01T00:00:00Z"}`,
				`{"type":"content","content":"hello","role":"assistant","timestamp":"2026-01-01T00:00:01Z"}`,
				`{"type":"result","success":true,"summary":"done","timestamp":"2026-01-01T00:00:02Z"}`,
			}, "\n"),
			wantEvents:    3,
			wantFirstType: EventToolCall,
			wantLastType:  EventResult,
		},
		{
			name: "malformed lines are skipped",
			input: strings.Join([]string{
				`{"type":"tool_use","tool":"Bash","timestamp":"2026-01-01T00:00:00Z"}`,
				`this is not json`,
				`{"type":"cost","input_tokens":100,"output_tokens":50,"cost_usd":0.01,"timestamp":"2026-01-01T00:00:01Z"}`,
			}, "\n"),
			wantEvents:    2,
			wantFirstType: EventToolCall,
			wantLastType:  EventCost,
		},
		{
			name:       "empty input produces no events",
			input:      "",
			wantEvents: 0,
		},
		{
			name: "blank lines are skipped",
			input: strings.Join([]string{
				"",
				`{"type":"cost","input_tokens":50,"output_tokens":25,"cost_usd":0.005,"timestamp":"2026-01-01T00:00:00Z"}`,
				"",
			}, "\n"),
			wantEvents:    1,
			wantFirstType: EventCost,
			wantLastType:  EventCost,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			reader := NewReader(logger)

			stream := nopCloser{strings.NewReader(tt.input)}
			eventCh := make(chan *StreamEvent, 100)

			err := reader.ReadStream(context.Background(), stream, eventCh)
			require.NoError(t, err)
			close(eventCh)

			var events []*StreamEvent
			for ev := range eventCh {
				events = append(events, ev)
			}

			assert.Len(t, events, tt.wantEvents)

			if tt.wantEvents > 0 {
				assert.Equal(t, tt.wantFirstType, events[0].Type)
				assert.Equal(t, tt.wantLastType, events[len(events)-1].Type)
			}
		})
	}
}

func TestReadStream_ContextCancellation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reader := NewReader(logger)

	// Use a pipe so we can control when data arrives.
	pr, pw := io.Pipe()

	eventCh := make(chan *StreamEvent, 10)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- reader.ReadStream(ctx, pr, eventCh)
	}()

	// Write one line, then cancel.
	_, err := pw.Write([]byte(`{"type":"cost","input_tokens":1,"output_tokens":1,"cost_usd":0.001,"timestamp":"2026-01-01T00:00:00Z"}` + "\n"))
	require.NoError(t, err)

	// Wait for the event to be received.
	select {
	case ev := <-eventCh:
		assert.Equal(t, EventCost, ev.Type)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	cancel()
	pw.Close()

	select {
	case readErr := <-errCh:
		// Either context.Canceled or nil (if pipe closed first) is acceptable.
		if readErr != nil {
			assert.ErrorIs(t, readErr, context.Canceled)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ReadStream to return")
	}
}
