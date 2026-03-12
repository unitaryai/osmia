package prm

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/unitaryai/osmia/internal/agentstream"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func makeToolEvent(tool string) *agentstream.StreamEvent {
	return &agentstream.StreamEvent{
		Type:      agentstream.EventToolCall,
		Timestamp: time.Now(),
		Parsed:    &agentstream.ToolCallEvent{Tool: tool},
	}
}

func TestScoreStep(t *testing.T) {
	tests := []struct {
		name     string
		tools    []string
		minScore int
		maxScore int
	}{
		{
			name:     "empty events yields baseline",
			tools:    nil,
			minScore: 5,
			maxScore: 5,
		},
		{
			name:     "productive read then edit pattern",
			tools:    []string{"Read", "Edit"},
			minScore: 7,
			maxScore: 10,
		},
		{
			name:     "productive edit then test pattern",
			tools:    []string{"Edit", "Bash"},
			minScore: 7,
			maxScore: 10,
		},
		{
			name:     "full productive cycle",
			tools:    []string{"Read", "Edit", "Bash"},
			minScore: 7,
			maxScore: 10,
		},
		{
			name:     "repeated identical calls",
			tools:    []string{"Bash", "Bash", "Bash", "Bash", "Bash"},
			minScore: 1,
			maxScore: 4,
		},
		{
			name:     "highly repetitive sequence",
			tools:    []string{"Read", "Read", "Read", "Read", "Read", "Read", "Read"},
			minScore: 1,
			maxScore: 4,
		},
		{
			name:     "diverse tool usage",
			tools:    []string{"Read", "Grep", "Edit", "Bash", "Glob"},
			minScore: 5,
			maxScore: 10,
		},
		{
			name:     "mixed productive and repetitive",
			tools:    []string{"Read", "Edit", "Edit", "Edit", "Edit", "Bash"},
			minScore: 3,
			maxScore: 8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scorer := NewScorer(testLogger(), 10)

			var events []*agentstream.StreamEvent
			for _, tool := range tt.tools {
				events = append(events, makeToolEvent(tool))
			}

			score := scorer.ScoreStep(events)
			assert.GreaterOrEqual(t, score.Score, tt.minScore, "score should be >= %d, got %d", tt.minScore, score.Score)
			assert.LessOrEqual(t, score.Score, tt.maxScore, "score should be <= %d, got %d", tt.maxScore, score.Score)
			assert.NotEmpty(t, score.Reasoning)
			assert.False(t, score.Timestamp.IsZero())
		})
	}
}

func TestMaxConsecutiveRepeats(t *testing.T) {
	tests := []struct {
		name  string
		tools []string
		want  int
	}{
		{name: "empty", tools: nil, want: 0},
		{name: "single", tools: []string{"a"}, want: 1},
		{name: "no repeats", tools: []string{"a", "b", "c"}, want: 1},
		{name: "all same", tools: []string{"a", "a", "a"}, want: 3},
		{name: "repeat in middle", tools: []string{"a", "b", "b", "b", "c"}, want: 3},
		{name: "repeat at end", tools: []string{"a", "b", "c", "c"}, want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maxConsecutiveRepeats(tt.tools)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHasPattern(t *testing.T) {
	tests := []struct {
		name  string
		tools []string
		predA func(string) bool
		predB func(string) bool
		want  bool
	}{
		{
			name:  "read followed by edit",
			tools: []string{"Read", "Edit"},
			predA: isReadTool,
			predB: isEditTool,
			want:  true,
		},
		{
			name:  "edit followed by test",
			tools: []string{"Edit", "Bash"},
			predA: isEditTool,
			predB: isTestTool,
			want:  true,
		},
		{
			name:  "no matching pattern",
			tools: []string{"Edit", "Read"},
			predA: isReadTool,
			predB: isEditTool,
			want:  false,
		},
		{
			name:  "empty tools",
			tools: nil,
			predA: isReadTool,
			predB: isEditTool,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasPattern(tt.tools, tt.predA, tt.predB)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCountUnique(t *testing.T) {
	tests := []struct {
		name  string
		tools []string
		want  int
	}{
		{name: "empty", tools: nil, want: 0},
		{name: "all unique", tools: []string{"a", "b", "c"}, want: 3},
		{name: "all same", tools: []string{"a", "a", "a"}, want: 1},
		{name: "some duplicates", tools: []string{"a", "b", "a", "c", "b"}, want: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countUnique(tt.tools)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToolClassifiers(t *testing.T) {
	readTools := []string{"Read", "read", "cat", "Glob", "Grep", "grep", "find", "ls"}
	for _, tool := range readTools {
		assert.True(t, isReadTool(tool), "%q should be classified as a read tool", tool)
	}
	assert.False(t, isReadTool("Edit"))

	editTools := []string{"Edit", "edit", "Write", "write", "sed", "awk", "edit_file", "write_file"}
	for _, tool := range editTools {
		assert.True(t, isEditTool(tool), "%q should be classified as an edit tool", tool)
	}
	assert.False(t, isEditTool("Read"))

	testTools := []string{"Bash", "bash", "test", "pytest", "go_test", "npm_test"}
	for _, tool := range testTools {
		assert.True(t, isTestTool(tool), "%q should be classified as a test tool", tool)
	}
	assert.False(t, isTestTool("Read"))
}
