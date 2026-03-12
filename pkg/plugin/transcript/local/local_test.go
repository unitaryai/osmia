package local

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/agentstream"
)

// testLogger returns a silent logger suitable for use in tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// makeEvent constructs a minimal StreamEvent of the given type for testing.
func makeEvent(eventType agentstream.EventType) *agentstream.StreamEvent {
	return &agentstream.StreamEvent{
		Type:      eventType,
		Timestamp: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
}

// readLines returns all non-empty lines from the given file path.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if text := scanner.Text(); text != "" {
			lines = append(lines, text)
		}
	}
	require.NoError(t, scanner.Err())
	return lines
}

func TestLocalSink_Append_CreatesFileAndWritesLine(t *testing.T) {
	dir := t.TempDir()
	sink := NewLocalSink(dir, testLogger())
	ctx := context.Background()

	taskRunID := "run-001"
	event := makeEvent(agentstream.EventToolCall)

	err := sink.Append(ctx, taskRunID, event)
	require.NoError(t, err)

	expectedPath := filepath.Join(dir, taskRunID+".jsonl")
	assert.FileExists(t, expectedPath)

	lines := readLines(t, expectedPath)
	require.Len(t, lines, 1)

	// Verify the line is valid JSON with the expected structure.
	var entry map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &entry))

	assert.Contains(t, entry, "ts")
	assert.Contains(t, entry, "type")
	assert.Contains(t, entry, "data")

	var eventType string
	require.NoError(t, json.Unmarshal(entry["type"], &eventType))
	assert.Equal(t, string(agentstream.EventToolCall), eventType)
}

func TestLocalSink_Flush_AfterAppend_ClosesGracefully(t *testing.T) {
	dir := t.TempDir()
	sink := NewLocalSink(dir, testLogger())
	ctx := context.Background()

	taskRunID := "run-002"
	event := makeEvent(agentstream.EventResult)

	require.NoError(t, sink.Append(ctx, taskRunID, event))

	// Flush should succeed and remove the file handle from the map.
	err := sink.Flush(ctx, taskRunID)
	require.NoError(t, err)

	// The file on disk should still exist with its content intact.
	expectedPath := filepath.Join(dir, taskRunID+".jsonl")
	assert.FileExists(t, expectedPath)

	lines := readLines(t, expectedPath)
	assert.Len(t, lines, 1)

	// A second Flush for the same task run should be a no-op (file removed
	// from map after first Flush).
	err = sink.Flush(ctx, taskRunID)
	require.NoError(t, err)
}

func TestLocalSink_Flush_WithNoPriorAppend_IsNoOp(t *testing.T) {
	dir := t.TempDir()
	sink := NewLocalSink(dir, testLogger())
	ctx := context.Background()

	// Flush for a task run that never had any events appended must not error.
	err := sink.Flush(ctx, "run-never-seen")
	require.NoError(t, err)

	// No file should have been created.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestLocalSink_Append_SecondCallAppendsToSameFile(t *testing.T) {
	dir := t.TempDir()
	sink := NewLocalSink(dir, testLogger())
	ctx := context.Background()

	taskRunID := "run-003"
	first := makeEvent(agentstream.EventToolCall)
	second := makeEvent(agentstream.EventCost)

	require.NoError(t, sink.Append(ctx, taskRunID, first))
	require.NoError(t, sink.Append(ctx, taskRunID, second))

	expectedPath := filepath.Join(dir, taskRunID+".jsonl")
	lines := readLines(t, expectedPath)
	require.Len(t, lines, 2, "expected exactly two lines after two Append calls")

	// Verify each line carries the correct event type.
	types := []string{string(agentstream.EventToolCall), string(agentstream.EventCost)}
	for i, raw := range lines {
		var entry map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(raw), &entry))

		var got string
		require.NoError(t, json.Unmarshal(entry["type"], &got))
		assert.Equal(t, types[i], got, "line %d has unexpected event type", i+1)
	}

	// Only one file should exist for this task run.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}
