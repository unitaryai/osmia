//go:build integration

// Package integration_test contains integration tests that verify the
// memory subsystem's end-to-end behaviour: extraction, storage, graph
// operations, and query retrieval across multiple simulated task runs.
package integration_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/agentstream"
	"github.com/unitaryai/osmia/internal/memory"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/pkg/engine"
)

func memoryTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// TestMemoryEndToEnd simulates 10 task completions, extracts knowledge
// from each, stores it in the graph, and verifies that the accumulated
// memory is queryable with correct temporal weighting and tenant isolation.
func TestMemoryEndToEnd(t *testing.T) {
	t.Parallel()

	logger := memoryTestLogger()
	ctx := context.Background()

	// Use in-memory SQLite for the integration test.
	store, err := memory.NewSQLiteStore(":memory:", logger)
	require.NoError(t, err)
	defer store.Close()

	graph := memory.NewGraph(store, logger)
	extractor := memory.NewExtractor(logger)
	qe := memory.NewQueryEngine(graph, logger)

	// Simulate 10 task completions with varied outcomes.
	type taskSim struct {
		id     string
		engine string
		state  taskrun.State
		result *engine.TaskResult
		events []agentstream.StreamEvent
	}

	tasks := []taskSim{
		{id: "tr-001", engine: "claude-code", state: taskrun.StateSucceeded, result: &engine.TaskResult{Success: true, Summary: "fixed login bug"}},
		{id: "tr-002", engine: "claude-code", state: taskrun.StateSucceeded, result: &engine.TaskResult{Success: true, Summary: "added unit tests"}},
		{id: "tr-003", engine: "codex", state: taskrun.StateFailed, result: &engine.TaskResult{Success: false, Summary: "syntax error in generated code"}},
		{id: "tr-004", engine: "claude-code", state: taskrun.StateSucceeded, result: &engine.TaskResult{Success: true, Summary: "refactored database layer"}},
		{id: "tr-005", engine: "aider", state: taskrun.StateTimedOut, result: nil},
		{id: "tr-006", engine: "claude-code", state: taskrun.StateSucceeded, result: &engine.TaskResult{Success: true, Summary: "updated API endpoint"}},
		{id: "tr-007", engine: "codex", state: taskrun.StateSucceeded, result: &engine.TaskResult{Success: true, Summary: "implemented caching"}},
		{id: "tr-008", engine: "claude-code", state: taskrun.StateFailed, result: &engine.TaskResult{Success: false, Summary: "test regression"}},
		{id: "tr-009", engine: "claude-code", state: taskrun.StateSucceeded, result: &engine.TaskResult{Success: true, Summary: "fixed flaky test"}},
		{id: "tr-010", engine: "claude-code", state: taskrun.StateSucceeded, result: &engine.TaskResult{Success: true, Summary: "added monitoring"}},
	}

	// Add some tool events for a couple of tasks to test pattern extraction.
	bashEvents := make([]agentstream.StreamEvent, 6)
	for i := range bashEvents {
		bashEvents[i] = agentstream.StreamEvent{
			Type:   agentstream.EventToolCall,
			Parsed: &agentstream.ToolCallEvent{Tool: "Bash"},
		}
	}
	tasks[0].events = bashEvents
	tasks[3].events = bashEvents

	tenantID := "test-tenant"
	totalNodes := 0
	totalEdges := 0

	for _, sim := range tasks {
		tr := taskrun.New(sim.id, fmt.Sprintf("idem-%s", sim.id), fmt.Sprintf("ticket-%s", sim.id), sim.engine)
		tr.State = sim.state
		tr.CurrentEngine = sim.engine
		tr.Result = sim.result

		// For the failed tasks, ensure the retry count is exhausted so
		// IsTerminal returns true for StateFailed.
		if sim.state == taskrun.StateFailed {
			tr.RetryCount = tr.MaxRetries
		}

		nodes, edges, err := extractor.Extract(ctx, tr, sim.events)
		require.NoError(t, err, "extraction failed for %s", sim.id)

		for _, node := range nodes {
			// Assign tenant to all extracted nodes.
			switch n := node.(type) {
			case *memory.Fact:
				n.TenantID = tenantID
			case *memory.Pattern:
				n.TenantID = tenantID
			case *memory.EngineProfile:
				n.TenantID = tenantID
			}
			require.NoError(t, graph.AddNode(ctx, node))
		}
		for _, edge := range edges {
			require.NoError(t, graph.AddEdge(ctx, edge))
		}

		totalNodes += len(nodes)
		totalEdges += len(edges)
	}

	// Verify accumulated state.
	assert.Equal(t, totalNodes, graph.NodeCount(), "total nodes should match extractions")
	assert.Equal(t, totalEdges, graph.EdgeCount(), "total edges should match extractions")

	// Verify that the graph has nodes from each category.
	assert.Greater(t, graph.NodeCount(), 10, "should have accumulated many nodes")

	// Query for the test tenant.
	mc, err := qe.QueryForTask(ctx, "fix a bug", "https://github.com/test/repo", "", tenantID)
	require.NoError(t, err)
	require.NotNil(t, mc)

	assert.NotEmpty(t, mc.RelevantFacts, "should have relevant facts")
	assert.NotEmpty(t, mc.KnownIssues, "should have known issues from failures")
	assert.NotEmpty(t, mc.FormattedSection, "should produce formatted section")
	assert.Contains(t, mc.FormattedSection, "## Prior Knowledge")

	// Verify tenant isolation: querying a different tenant returns nothing.
	mcOther, err := qe.QueryForTask(ctx, "fix a bug", "https://github.com/test/repo", "", "other-tenant")
	require.NoError(t, err)
	assert.Empty(t, mcOther.RelevantFacts, "other tenant should see no facts")
	assert.Empty(t, mcOther.FormattedSection, "other tenant should have empty section")

	// Test confidence decay.
	graph.DecayConfidence(ctx)

	// Query again — results should still be present but with lower confidence.
	mcAfterDecay, err := qe.QueryForTask(ctx, "fix a bug", "", "", tenantID)
	require.NoError(t, err)
	assert.NotEmpty(t, mcAfterDecay.RelevantFacts, "facts should survive one decay cycle")

	// Reload from store before pruning to verify persistence.
	graph2 := memory.NewGraph(store, logger)
	require.NoError(t, graph2.LoadFromStore(ctx))
	assert.Greater(t, graph2.NodeCount(), 0, "reloaded graph should have nodes")

	// Test pruning with a high threshold (most nodes should be pruned after decay).
	pruned := graph.PruneStale(ctx, 0.99)
	assert.Greater(t, pruned, 0, "some nodes should be pruned at high threshold")
}

// TestMemoryDecayAndPruneLifecycle verifies that repeated decay cycles
// eventually bring nodes below the prune threshold and they are removed.
func TestMemoryDecayAndPruneLifecycle(t *testing.T) {
	t.Parallel()

	logger := memoryTestLogger()
	ctx := context.Background()

	store, err := memory.NewSQLiteStore(":memory:", logger)
	require.NoError(t, err)
	defer store.Close()

	graph := memory.NewGraph(store, logger)

	// Add a node with aggressive decay.
	require.NoError(t, graph.AddNode(ctx, &memory.Fact{
		ID:         "ephemeral",
		Content:    "short-lived fact",
		Confidence: 0.5,
		DecayRate:  0.2, // 20% decay per cycle
		ValidFrom:  time.Now(),
		TenantID:   "t",
	}))
	require.NoError(t, graph.AddNode(ctx, &memory.Fact{
		ID:         "durable",
		Content:    "long-lived fact",
		Confidence: 1.0,
		DecayRate:  0.01, // 1% decay per cycle
		ValidFrom:  time.Now(),
		TenantID:   "t",
	}))

	// Run 10 decay cycles.
	for i := 0; i < 10; i++ {
		graph.DecayConfidence(ctx)
	}

	// Prune nodes below 0.1 confidence.
	pruned := graph.PruneStale(ctx, 0.1)
	assert.Equal(t, 1, pruned, "ephemeral node should be pruned")
	assert.Equal(t, 1, graph.NodeCount(), "only durable node should remain")
	assert.NotNil(t, graph.GetNode("durable"))
	assert.Nil(t, graph.GetNode("ephemeral"))
}
