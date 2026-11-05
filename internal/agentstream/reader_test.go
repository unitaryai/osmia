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
			// A large tool result easily passes bufio.Scanner's 64 KiB limit,
			// which used to end the stream there.
			name: "line longer than 64 KiB is read, and so are the lines after it",
			input: strings.Join([]string{
				`{"type":"content","content":"` + strings.Repeat("x", 200<<10) + `","role":"assistant","timestamp":"2026-01-01T00:00:00Z"}`,
				`{"type":"result","success":true,"summary":"done","timestamp":"2026-01-01T00:00:01Z"}`,
			}, "\n"),
			wantEvents:    2,
			wantFirstType: EventContentDelta,
			wantLastType:  EventResult,
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

// Given a line over the reader's size limit,
// when the stream is read,
// then that line is skipped and the events after it still arrive.
func TestReadStream_SkipsLineOverLimit(t *testing.T) {
	reader := NewReader(slog.New(slog.NewTextHandler(io.Discard, nil)))
	reader.maxLineBytes = 1 << 10

	input := strings.Join([]string{
		`{"type":"content","content":"` + strings.Repeat("x", 4<<10) + `","role":"assistant","timestamp":"2026-01-01T00:00:00Z"}`,
		`{"type":"result","success":true,"summary":"done","timestamp":"2026-01-01T00:00:01Z"}`,
	}, "\n")
	eventCh := make(chan *StreamEvent, 10)

	require.NoError(t, reader.ReadStream(context.Background(), nopCloser{strings.NewReader(input)}, eventCh))
	close(eventCh)

	var types []EventType
	for ev := range eventCh {
		types = append(types, ev.Type)
	}
	assert.Equal(t, []EventType{EventResult}, types)
}

// stubTranslator is a test Translator that lets each line be mapped to an
// arbitrary number of events (including zero), so the reader's delegation
// to a non-passthrough Translator can be exercised without a real engine
// stream format.
type stubTranslator struct {
	// translate is called for every non-empty line the reader scans.
	translate func(line []byte) ([]*StreamEvent, error)
}

func (s stubTranslator) Translate(line []byte) ([]*StreamEvent, error) {
	return s.translate(line)
}

// TestReadStream_WithTranslator asserts that ReadStream delegates line
// translation to a configured Translator instead of always parsing lines
// as Osmia's native NDJSON envelope, and that a translator producing
// multiple (or zero) events per line is handled correctly.
func TestReadStream_WithTranslator(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	translator := stubTranslator{
		translate: func(line []byte) ([]*StreamEvent, error) {
			switch string(line) {
			case "skip-me":
				return nil, nil
			case "boom":
				return nil, assert.AnError
			default:
				// Emit two events per line to prove the reader forwards
				// every event a Translator returns, not just one.
				return []*StreamEvent{
					{Type: EventContentDelta},
					{Type: EventCost},
				}, nil
			}
		},
	}

	reader := NewReader(logger, WithTranslator(translator))

	input := strings.Join([]string{"skip-me", "boom", "line"}, "\n")
	stream := nopCloser{strings.NewReader(input)}
	eventCh := make(chan *StreamEvent, 100)

	err := reader.ReadStream(context.Background(), stream, eventCh)
	require.NoError(t, err)
	close(eventCh)

	var events []*StreamEvent
	for ev := range eventCh {
		events = append(events, ev)
	}

	require.Len(t, events, 2)
	assert.Equal(t, EventContentDelta, events[0].Type)
	assert.Equal(t, EventCost, events[1].Type)
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
