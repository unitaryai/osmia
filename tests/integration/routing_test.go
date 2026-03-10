//go:build integration

package integration_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/routing"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// TestIntelligentRoutingEndToEnd feeds 20 task outcomes and verifies
// the router selects the correct engine for known task types.
func TestIntelligentRoutingEndToEnd(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	store := routing.NewMemoryFingerprintStore()
	fallback := &staticFallback{engines: []string{"claude-code", "aider", "codex"}}
	cfg := &config.RoutingConfig{
		Enabled:              true,
		EpsilonGreedy:        0.0, // deterministic for testing
		MinSamplesForRouting: 5,
	}

	sel := routing.NewIntelligentSelector(
		store, fallback, cfg,
		[]string{"claude-code", "aider", "codex"},
		logger,
	)

	// Scenario: claude-code excels at bug_fix in Go.
	for i := 0; i < 8; i++ {
		require.NoError(t, sel.RecordOutcome(ctx, routing.TaskOutcome{
			EngineName:   "claude-code",
			TaskType:     "bug_fix",
			RepoLanguage: "go",
			RepoSize:     200,
			Complexity:   "low",
			Success:      true,
			Duration:     5 * time.Minute,
			Cost:         1.0,
		}))
	}
	// claude-code has some failures with refactors.
	for i := 0; i < 4; i++ {
		require.NoError(t, sel.RecordOutcome(ctx, routing.TaskOutcome{
			EngineName:   "claude-code",
			TaskType:     "refactor",
			RepoLanguage: "go",
			RepoSize:     200,
			Complexity:   "high",
			Success:      i < 1, // 25% success for refactors
			Duration:     20 * time.Minute,
			Cost:         5.0,
		}))
	}

	// Scenario: aider is great at refactors.
	for i := 0; i < 8; i++ {
		require.NoError(t, sel.RecordOutcome(ctx, routing.TaskOutcome{
			EngineName:   "aider",
			TaskType:     "refactor",
			RepoLanguage: "go",
			RepoSize:     200,
			Complexity:   "high",
			Success:      true,
			Duration:     15 * time.Minute,
			Cost:         3.0,
		}))
	}
	// aider has some failures with bug_fix.
	for i := 0; i < 4; i++ {
		require.NoError(t, sel.RecordOutcome(ctx, routing.TaskOutcome{
			EngineName:   "aider",
			TaskType:     "bug_fix",
			RepoLanguage: "go",
			RepoSize:     200,
			Complexity:   "low",
			Success:      i < 1, // 25% success for bug fixes
			Duration:     10 * time.Minute,
			Cost:         2.0,
		}))
	}

	// codex has minimal data (below threshold) — should not influence routing.
	require.NoError(t, sel.RecordOutcome(ctx, routing.TaskOutcome{
		EngineName: "codex",
		TaskType:   "bug_fix",
		Success:    true,
	}))

	// Test 1: Bug fix in Go should prefer claude-code.
	bugFixTicket := ticketing.Ticket{
		ID:         "T-BUG-1",
		TicketType: "bug_fix",
		Labels:     []string{"lang:go"},
	}
	result := sel.SelectEngines(bugFixTicket)
	require.NotEmpty(t, result)
	assert.Equal(t, "claude-code", result[0],
		"claude-code should be preferred for Go bug fixes")

	// Test 2: Refactor in Go should prefer aider.
	refactorTicket := ticketing.Ticket{
		ID:         "T-REF-1",
		TicketType: "refactor",
		Labels:     []string{"lang:go", "complexity:high"},
	}
	result = sel.SelectEngines(refactorTicket)
	require.NotEmpty(t, result)
	assert.Equal(t, "aider", result[0],
		"aider should be preferred for Go refactors")

	// Test 3: All engines should appear in the result for fallback.
	assert.Len(t, result, 3, "all available engines should be in the result chain")

	// Test 4: Verify fingerprint store has data for engines that were used.
	fps, err := store.List(ctx)
	require.NoError(t, err)
	assert.Len(t, fps, 3, "should have fingerprints for all three engines")
}

// staticFallback implements routing.FallbackSelector for integration tests.
type staticFallback struct {
	engines []string
}

func (s *staticFallback) SelectEngines(_ ticketing.Ticket) []string {
	return s.engines
}
