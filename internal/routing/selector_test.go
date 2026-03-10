package routing

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// mockFallback implements FallbackSelector for testing.
type mockFallback struct {
	engines []string
}

func (m *mockFallback) SelectEngines(_ ticketing.Ticket) []string {
	return m.engines
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestIntelligentSelector_FallsBackWithInsufficientData(t *testing.T) {
	store := NewMemoryFingerprintStore()
	fallback := &mockFallback{engines: []string{"claude-code", "aider"}}
	cfg := &config.RoutingConfig{
		Enabled:              true,
		EpsilonGreedy:        0.1,
		MinSamplesForRouting: 5,
	}

	sel := NewIntelligentSelector(store, fallback, cfg, []string{"claude-code", "aider"}, testLogger())

	ticket := ticketing.Ticket{
		ID:         "T-1",
		TicketType: "bug_fix",
		Labels:     []string{"lang:go"},
	}

	result := sel.SelectEngines(ticket)
	assert.Equal(t, []string{"claude-code", "aider"}, result,
		"should fall back to static selector when no fingerprint data")
}

func TestIntelligentSelector_UsesFingerprints(t *testing.T) {
	store := NewMemoryFingerprintStore()
	ctx := context.Background()

	// Build fingerprints: claude-code is great at bug_fix, aider is mediocre.
	claudeFP := NewEngineFingerprint("claude-code")
	for i := 0; i < 10; i++ {
		claudeFP.Update(TaskOutcome{
			EngineName:   "claude-code",
			TaskType:     "bug_fix",
			RepoLanguage: "go",
			RepoSize:     50,
			Complexity:   "low",
			Success:      true,
		})
	}
	require.NoError(t, store.Save(ctx, claudeFP))

	aiderFP := NewEngineFingerprint("aider")
	for i := 0; i < 10; i++ {
		aiderFP.Update(TaskOutcome{
			EngineName:   "aider",
			TaskType:     "bug_fix",
			RepoLanguage: "go",
			RepoSize:     50,
			Complexity:   "low",
			Success:      i < 3, // 30% success
		})
	}
	require.NoError(t, store.Save(ctx, aiderFP))

	fallback := &mockFallback{engines: []string{"aider", "claude-code"}}
	cfg := &config.RoutingConfig{
		Enabled:              true,
		EpsilonGreedy:        0.0, // disable exploration for deterministic test
		MinSamplesForRouting: 5,
	}

	sel := NewIntelligentSelector(store, fallback, cfg, []string{"claude-code", "aider"}, testLogger())

	ticket := ticketing.Ticket{
		ID:         "T-2",
		TicketType: "bug_fix",
		Labels:     []string{"lang:go"},
	}

	result := sel.SelectEngines(ticket)
	require.Len(t, result, 2)
	assert.Equal(t, "claude-code", result[0],
		"claude-code should be ranked first with higher success rate")
	assert.Equal(t, "aider", result[1])
}

func TestIntelligentSelector_EpsilonGreedyExploration(t *testing.T) {
	store := NewMemoryFingerprintStore()
	ctx := context.Background()

	// Both engines have sufficient data but claude-code is better.
	claudeFP := NewEngineFingerprint("claude-code")
	for i := 0; i < 20; i++ {
		claudeFP.Update(TaskOutcome{
			EngineName: "claude-code",
			TaskType:   "bug_fix",
			Success:    true,
		})
	}
	require.NoError(t, store.Save(ctx, claudeFP))

	aiderFP := NewEngineFingerprint("aider")
	for i := 0; i < 20; i++ {
		aiderFP.Update(TaskOutcome{
			EngineName: "aider",
			TaskType:   "bug_fix",
			Success:    i < 5, // 25% success
		})
	}
	require.NoError(t, store.Save(ctx, aiderFP))

	cfg := &config.RoutingConfig{
		Enabled:              true,
		EpsilonGreedy:        1.0, // always explore
		MinSamplesForRouting: 5,
	}

	sel := NewIntelligentSelector(store, nil, cfg, []string{"claude-code", "aider"}, testLogger())

	ticket := ticketing.Ticket{
		ID:         "T-3",
		TicketType: "bug_fix",
	}

	// With epsilon=1.0, the top engine should sometimes be overridden.
	// Run multiple times and verify aider appears at least once in position 0.
	aiderFirst := false
	for i := 0; i < 50; i++ {
		result := sel.SelectEngines(ticket)
		if result[0] == "aider" {
			aiderFirst = true
			break
		}
	}
	assert.True(t, aiderFirst,
		"with epsilon=1.0, exploration should sometimes select aider first")
}

func TestIntelligentSelector_RecordOutcome(t *testing.T) {
	store := NewMemoryFingerprintStore()
	cfg := &config.RoutingConfig{
		Enabled:              true,
		EpsilonGreedy:        0.1,
		MinSamplesForRouting: 5,
	}

	sel := NewIntelligentSelector(store, nil, cfg, []string{"claude-code"}, testLogger())
	ctx := context.Background()

	err := sel.RecordOutcome(ctx, TaskOutcome{
		EngineName:   "claude-code",
		TaskType:     "bug_fix",
		RepoLanguage: "python",
		Success:      true,
		Duration:     10 * time.Minute,
		Cost:         2.50,
	})
	require.NoError(t, err)

	// Verify fingerprint was persisted.
	fp, err := store.Get(ctx, "claude-code")
	require.NoError(t, err)
	assert.Equal(t, 1, fp.TotalTasks)
}

func TestExtractLanguageLabel(t *testing.T) {
	tests := []struct {
		name     string
		labels   []string
		expected string
	}{
		{"with lang label", []string{"osmia", "lang:python"}, "python"},
		{"no lang label", []string{"osmia", "priority:high"}, ""},
		{"empty labels", nil, ""},
		{"short label", []string{"lang"}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, extractLanguageLabel(tt.labels))
		})
	}
}

func TestExtractComplexityLabel(t *testing.T) {
	tests := []struct {
		name     string
		labels   []string
		expected string
	}{
		{"with complexity label", []string{"complexity:high"}, "high"},
		{"no complexity label", []string{"lang:go"}, ""},
		{"empty labels", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, extractComplexityLabel(tt.labels))
		})
	}
}

func TestQueryFromTicket(t *testing.T) {
	ticket := ticketing.Ticket{
		TicketType: "bug_fix",
		Labels:     []string{"lang:rust", "complexity:medium"},
	}

	q := queryFromTicket(ticket)
	assert.Equal(t, "bug_fix", q.TaskType)
	assert.Equal(t, "rust", q.RepoLanguage)
	assert.Equal(t, "medium", q.Complexity)
}
