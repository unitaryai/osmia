//go:build integration

// Package integration_test contains Tier 3 integration tests that verify
// the streaming pipeline, from NDJSON Reader through Forwarder to watchdog
// event consumption and TaskRun telemetry updates.
package integration_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/internal/watchdog"
)

// streamTestLogger returns a logger suitable for integration tests.
func streamTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// nopCloser wraps an io.Reader to satisfy io.ReadCloser.
type nopCloser struct {
	io.Reader
}

func (nopCloser) Close() error { return nil }

// TestStreamingFullPipeline verifies that events flow from an NDJSON
// stream through Reader → Forwarder → watchdog channel, preserving order
// and type fidelity across all event types.
func TestStreamingFullPipeline(t *testing.T) {
	t.Parallel()

	lines := []string{
		`{"type":"tool_use","tool":"Bash","args":{"command":"ls"},"timestamp":"2026-01-15T10:00:00Z"}`,
		`{"type":"content","content":"listing files","role":"assistant","timestamp":"2026-01-15T10:00:01Z"}`,
		`{"type":"cost","input_tokens":500,"output_tokens":200,"cost_usd":0.05,"timestamp":"2026-01-15T10:00:02Z"}`,
		`{"type":"tool_use","tool":"Read","args":{"file":"main.go"},"timestamp":"2026-01-15T10:00:03Z"}`,
		`{"type":"result","success":true,"summary":"all done","timestamp":"2026-01-15T10:00:04Z"}`,
	}

	logger := streamTestLogger()
	reader := agentstream.NewReader(logger)

	stream := nopCloser{strings.NewReader(strings.Join(lines, "\n"))}
	eventCh := make(chan *agentstream.StreamEvent, 100)

	// Read all events from the stream.
	err := reader.ReadStream(context.Background(), stream, eventCh)
	require.NoError(t, err)
	close(eventCh)

	// Collect events from the eventCh and re-feed them through the forwarder.
	var readEvents []*agentstream.StreamEvent
	for ev := range eventCh {
		readEvents = append(readEvents, ev)
	}
	require.Len(t, readEvents, 5, "reader must emit all valid events")

	// Feed collected events through the forwarder with a watchdog channel.
	watchdogCh := make(chan *agentstream.StreamEvent, 100)
	fwd := agentstream.NewForwarder(logger, agentstream.WithWatchdogChannel(watchdogCh))

	fwdCh := make(chan *agentstream.StreamEvent, len(readEvents))
	for _, ev := range readEvents {
		fwdCh <- ev
	}
	close(fwdCh)

	err = fwd.Forward(context.Background(), fwdCh)
	require.NoError(t, err)
	close(watchdogCh)

	// Verify the watchdog channel received all events in order.
	var watchdogEvents []*agentstream.StreamEvent
	for ev := range watchdogCh {
		watchdogEvents = append(watchdogEvents, ev)
	}
	require.Len(t, watchdogEvents, 5, "watchdog must receive all forwarded events")

	expectedTypes := []agentstream.EventType{
		agentstream.EventToolCall,
		agentstream.EventContentDelta,
		agentstream.EventCost,
		agentstream.EventToolCall,
		agentstream.EventResult,
	}
	for i, wantType := range expectedTypes {
		assert.Equal(t, wantType, watchdogEvents[i].Type, "event %d type mismatch", i)
	}
}

// TestStreamingMixedValidInvalid verifies that malformed JSON lines in
// the NDJSON stream are skipped gracefully while valid events still reach
// the watchdog channel.
func TestStreamingMixedValidInvalid(t *testing.T) {
	t.Parallel()

	lines := []string{
		`{"type":"tool_use","tool":"Bash","timestamp":"2026-01-15T10:00:00Z"}`,
		`this is not valid json`,
		`{"broken json`,
		`{"type":"cost","input_tokens":100,"output_tokens":50,"cost_usd":0.01,"timestamp":"2026-01-15T10:00:01Z"}`,
		``,
		`{"type":"result","success":true,"summary":"completed","timestamp":"2026-01-15T10:00:02Z"}`,
	}

	logger := streamTestLogger()
	reader := agentstream.NewReader(logger)

	stream := nopCloser{strings.NewReader(strings.Join(lines, "\n"))}
	eventCh := make(chan *agentstream.StreamEvent, 100)

	err := reader.ReadStream(context.Background(), stream, eventCh)
	require.NoError(t, err)
	close(eventCh)

	var events []*agentstream.StreamEvent
	for ev := range eventCh {
		events = append(events, ev)
	}

	// Only three valid lines: tool_use, cost, result.
	require.Len(t, events, 3, "only valid events should pass through")
	assert.Equal(t, agentstream.EventToolCall, events[0].Type)
	assert.Equal(t, agentstream.EventCost, events[1].Type)
	assert.Equal(t, agentstream.EventResult, events[2].Type)
}

// TestStreamingWatchdogEventConsumption verifies that the watchdog's
// ConsumeStreamEvent method correctly updates TaskRun telemetry fields
// for each event type.
func TestStreamingWatchdogEventConsumption(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		event  *agentstream.StreamEvent
		setup  func(tr *taskrun.TaskRun)
		assert func(t *testing.T, tr *taskrun.TaskRun)
	}{
		{
			name: "tool call increments ToolCallsTotal",
			event: &agentstream.StreamEvent{
				Type: agentstream.EventToolCall,
				Parsed: &agentstream.ToolCallEvent{
					Tool: "Bash",
				},
			},
			setup: func(tr *taskrun.TaskRun) {
				tr.ToolCallsTotal = 3
				tr.LastToolName = "Read"
				tr.ConsecutiveIdenticalTools = 2
			},
			assert: func(t *testing.T, tr *taskrun.TaskRun) {
				assert.Equal(t, 4, tr.ToolCallsTotal, "tool call count should increment")
				assert.Equal(t, "Bash", tr.LastToolName, "last tool name should update")
				assert.Equal(t, 1, tr.ConsecutiveIdenticalTools, "different tool resets consecutive count")
			},
		},
		{
			name: "cost event updates TokensConsumed",
			event: &agentstream.StreamEvent{
				Type: agentstream.EventCost,
				Parsed: &agentstream.CostEvent{
					InputTokens:  10000,
					OutputTokens: 3000,
					CostUSD:      0.50,
				},
			},
			setup: func(tr *taskrun.TaskRun) {
				tr.TokensConsumed = 500
			},
			assert: func(t *testing.T, tr *taskrun.TaskRun) {
				assert.Equal(t, 13000, tr.TokensConsumed, "tokens should be sum of input + output")
			},
		},
		{
			name: "content delta updates HeartbeatAt",
			event: &agentstream.StreamEvent{
				Type: agentstream.EventContentDelta,
				Parsed: &agentstream.ContentDeltaEvent{
					Content: "processing request",
					Role:    "assistant",
				},
			},
			setup: func(_ *taskrun.TaskRun) {},
			assert: func(t *testing.T, tr *taskrun.TaskRun) {
				require.NotNil(t, tr.HeartbeatAt, "heartbeat should be set after content delta")
				assert.WithinDuration(t, time.Now(), *tr.HeartbeatAt, 2*time.Second)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := watchdog.DefaultConfig()
			w := watchdog.New(cfg, streamTestLogger())

			tr := taskrun.New("tr-stream-"+tt.name, "idem-1", "ticket-1", "claude-code")
			tt.setup(tr)
			w.ConsumeStreamEvent(tr, tt.event)
			tt.assert(t, tr)
		})
	}
}

// TestStreamingMultiEventSequence simulates a realistic agent run with
// multiple tool calls, content deltas, cost updates, and a final result,
// verifying that the TaskRun's telemetry state is correct at the end.
func TestStreamingMultiEventSequence(t *testing.T) {
	t.Parallel()

	events := []*agentstream.StreamEvent{
		// 5 tool calls: Bash, Read, Bash, Write, Bash
		{Type: agentstream.EventToolCall, Parsed: &agentstream.ToolCallEvent{Tool: "Bash"}},
		{Type: agentstream.EventToolCall, Parsed: &agentstream.ToolCallEvent{Tool: "Read"}},
		{Type: agentstream.EventToolCall, Parsed: &agentstream.ToolCallEvent{Tool: "Bash"}},
		{Type: agentstream.EventToolCall, Parsed: &agentstream.ToolCallEvent{Tool: "Write"}},
		{Type: agentstream.EventToolCall, Parsed: &agentstream.ToolCallEvent{Tool: "Bash"}},
		// 3 content deltas
		{Type: agentstream.EventContentDelta, Parsed: &agentstream.ContentDeltaEvent{Content: "analysing code", Role: "assistant"}},
		{Type: agentstream.EventContentDelta, Parsed: &agentstream.ContentDeltaEvent{Content: "making changes", Role: "assistant"}},
		{Type: agentstream.EventContentDelta, Parsed: &agentstream.ContentDeltaEvent{Content: "running tests", Role: "assistant"}},
		// 2 cost events
		{Type: agentstream.EventCost, Parsed: &agentstream.CostEvent{InputTokens: 5000, OutputTokens: 2000, CostUSD: 0.25}},
		{Type: agentstream.EventCost, Parsed: &agentstream.CostEvent{InputTokens: 12000, OutputTokens: 4500, CostUSD: 0.60}},
		// 1 result
		{Type: agentstream.EventResult, Parsed: &agentstream.ResultEvent{Success: true, Summary: "task completed successfully"}},
	}

	cfg := watchdog.DefaultConfig()
	w := watchdog.New(cfg, streamTestLogger())

	tr := taskrun.New("tr-multi", "idem-multi", "ticket-multi", "claude-code")

	for _, ev := range events {
		w.ConsumeStreamEvent(tr, ev)
	}

	// Verify final TaskRun state.
	assert.Equal(t, 5, tr.ToolCallsTotal, "should have 5 total tool calls")
	assert.Equal(t, "Bash", tr.LastToolName, "last tool should be Bash")

	// Last three tool calls: Bash, Write, Bash — consecutive identical is 1
	// because Write breaks the Bash chain.
	assert.Equal(t, 1, tr.ConsecutiveIdenticalTools, "final consecutive count for Bash after Write break")

	// Last cost event: input=12000 output=4500 → total=16500
	assert.Equal(t, 16500, tr.TokensConsumed, "tokens should reflect last cost event sum")

	// HeartbeatAt should have been set by the content delta events.
	require.NotNil(t, tr.HeartbeatAt, "heartbeat should be set by content deltas")
	assert.WithinDuration(t, time.Now(), *tr.HeartbeatAt, 2*time.Second)
}
