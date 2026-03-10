package diagnosis

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/pkg/engine"
)

func diagTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func makeEvent(parsed any) *agentstream.StreamEvent {
	return &agentstream.StreamEvent{Parsed: parsed}
}

func TestAnalyser_FailureModeClassification(t *testing.T) {
	ctx := context.Background()
	analyser := NewAnalyser(diagTestLogger())

	tests := []struct {
		name     string
		input    DiagnosisInput
		wantMode FailureMode
	}{
		{
			name: "dependency missing from event content",
			input: DiagnosisInput{
				TaskRun: taskrun.New("tr-1", "idem-1", "ticket-1", "claude-code"),
				Events: []*agentstream.StreamEvent{
					makeEvent(&agentstream.ContentDeltaEvent{
						Content: "Error: module not found: github.com/foo/bar",
					}),
				},
			},
			wantMode: DependencyMissing,
		},
		{
			name: "permission blocked from event content",
			input: DiagnosisInput{
				TaskRun: taskrun.New("tr-2", "idem-2", "ticket-2", "claude-code"),
				Events: []*agentstream.StreamEvent{
					makeEvent(&agentstream.ContentDeltaEvent{
						Content: "open /etc/shadow: permission denied",
					}),
				},
			},
			wantMode: PermissionBlocked,
		},
		{
			name: "infra failure from watchdog reason",
			input: DiagnosisInput{
				TaskRun:        taskrun.New("tr-3", "idem-3", "ticket-3", "claude-code"),
				WatchdogReason: "container OOMKilled",
			},
			wantMode: InfraFailure,
		},
		{
			name: "infra failure from timeout",
			input: DiagnosisInput{
				TaskRun:        taskrun.New("tr-4", "idem-4", "ticket-4", "claude-code"),
				WatchdogReason: "deadline exceeded",
			},
			wantMode: InfraFailure,
		},
		{
			name: "model confusion from high consecutive calls",
			input: DiagnosisInput{
				TaskRun: func() *taskrun.TaskRun {
					tr := taskrun.New("tr-5", "idem-5", "ticket-5", "claude-code")
					tr.ConsecutiveIdenticalTools = 10
					return tr
				}(),
			},
			wantMode: ModelConfusion,
		},
		{
			name: "scope creep from excessive files changed",
			input: DiagnosisInput{
				TaskRun: func() *taskrun.TaskRun {
					tr := taskrun.New("tr-6", "idem-6", "ticket-6", "claude-code")
					tr.FilesChanged = 30
					return tr
				}(),
			},
			wantMode: ScopeCreep,
		},
		{
			name: "test misunderstanding from editing test files",
			input: DiagnosisInput{
				TaskRun: taskrun.New("tr-7", "idem-7", "ticket-7", "claude-code"),
				Events: []*agentstream.StreamEvent{
					makeEvent(&agentstream.ToolCallEvent{
						Tool: "Edit",
						Args: json.RawMessage(`{"path": "internal/foo_test.go"}`),
					}),
				},
			},
			wantMode: TestMisunderstanding,
		},
		{
			name: "wrong approach from failed result",
			input: DiagnosisInput{
				TaskRun: taskrun.New("tr-8", "idem-8", "ticket-8", "claude-code"),
				Result: &engine.TaskResult{
					Success: false,
					Summary: "tests failed: unexpected output",
				},
			},
			wantMode: WrongApproach,
		},
		{
			name: "unknown when no pattern matches",
			input: DiagnosisInput{
				TaskRun: taskrun.New("tr-9", "idem-9", "ticket-9", "claude-code"),
				Result: &engine.TaskResult{
					Success: false,
				},
			},
			wantMode: Unknown,
		},
		{
			name: "dependency missing from ModuleNotFoundError",
			input: DiagnosisInput{
				TaskRun: taskrun.New("tr-10", "idem-10", "ticket-10", "codex"),
				Events: []*agentstream.StreamEvent{
					makeEvent(&agentstream.ContentDeltaEvent{
						Content: "ModuleNotFoundError: No module named 'requests'",
					}),
				},
			},
			wantMode: DependencyMissing,
		},
		{
			name: "permission blocked from EACCES",
			input: DiagnosisInput{
				TaskRun: taskrun.New("tr-11", "idem-11", "ticket-11", "claude-code"),
				Events: []*agentstream.StreamEvent{
					makeEvent(&agentstream.ContentDeltaEvent{
						Content: "Error: EACCES: permission denied, open '/root/.config'",
					}),
				},
			},
			wantMode: PermissionBlocked,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diag, err := analyser.Analyse(ctx, tt.input)
			require.NoError(t, err)
			require.NotNil(t, diag)
			assert.Equal(t, tt.wantMode, diag.Mode)
			assert.NotEmpty(t, diag.Evidence)
			assert.True(t, diag.Confidence > 0, "confidence should be positive")
			assert.False(t, diag.DiagnosedAt.IsZero(), "diagnosed_at should be set")
		})
	}
}

func TestAnalyser_InfraHasHighestPriority(t *testing.T) {
	ctx := context.Background()
	analyser := NewAnalyser(diagTestLogger())

	// Input with both infra AND dependency signals — infra should win.
	input := DiagnosisInput{
		TaskRun: taskrun.New("tr-prio", "idem-prio", "ticket-prio", "claude-code"),
		Events: []*agentstream.StreamEvent{
			makeEvent(&agentstream.ContentDeltaEvent{
				Content: "module not found error followed by OOMKilled",
			}),
		},
	}

	diag, err := analyser.Analyse(ctx, input)
	require.NoError(t, err)
	assert.Equal(t, InfraFailure, diag.Mode)
}

func TestExtractEventTexts(t *testing.T) {
	events := []*agentstream.StreamEvent{
		makeEvent(&agentstream.ContentDeltaEvent{Content: "hello"}),
		makeEvent(&agentstream.ResultEvent{Summary: "done"}),
		makeEvent(&agentstream.ToolCallEvent{Args: json.RawMessage(`{"x":1}`)}),
		makeEvent(nil), // nil Parsed should be skipped
	}

	texts := extractEventTexts(events)
	assert.Len(t, texts, 3)
	assert.Contains(t, texts, "hello")
	assert.Contains(t, texts, "done")
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		expect string
	}{
		{"short string", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"truncated", "hello world", 5, "hello..."},
		{"empty", "", 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, truncate(tt.input, tt.maxLen))
		})
	}
}

func TestMatchPatterns(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		patterns []string
		wantLen  int
	}{
		{"single match", "module not found in build", dependencyPatterns, 1},
		{"multiple matches", "module not found and import error", dependencyPatterns, 2},
		{"no match", "everything is fine", dependencyPatterns, 0},
		{"case insensitive", "MODULE NOT FOUND", dependencyPatterns, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := matchPatterns(tt.text, tt.patterns)
			assert.Len(t, matches, tt.wantLen)
		})
	}
}
