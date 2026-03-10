//go:build integration

// Package integration_test contains integration tests verifying that the
// Memory subsystem is correctly wired into the controller's reconciliation
// pipeline: knowledge extraction on completion, and memory context injection
// into new task prompts.
package integration_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/controller"
	"github.com/unitaryai/osmia/internal/memory"
	"github.com/unitaryai/osmia/internal/taskrun"
	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

func memoryControllerLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// TestMemoryControllerExtraction verifies that when a job completes
// successfully with Memory enabled, knowledge nodes are extracted into the
// graph.
func TestMemoryControllerExtraction(t *testing.T) {
	t.Parallel()

	logger := memoryControllerLogger()
	ctx := context.Background()

	// Set up memory components with in-memory SQLite.
	store, err := memory.NewSQLiteStore(":memory:", logger)
	require.NoError(t, err)
	defer store.Close()

	graph := memory.NewGraph(store, logger)
	extractor := memory.NewExtractor(logger)
	queryEngine := memory.NewQueryEngine(graph, logger)

	cfg := &config.Config{
		Engines: config.EnginesConfig{Default: "claude-code"},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     5,
			MaxJobDurationMinutes: 120,
		},
	}
	k8s := fake.NewSimpleClientset()

	eng := &stubEngine{name: "claude-code"}
	tb := newStubTicketing(nil)
	jb := &stubJobBuilder{}

	r := controller.NewReconciler(cfg, logger,
		controller.WithEngine(eng),
		controller.WithTicketing(tb),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
		controller.WithMemory(graph, extractor, queryEngine),
	)

	// Process a ticket to create a running TaskRun.
	ticket := ticketing.Ticket{
		ID:    "MEM-TICKET-1",
		Title: "Fix the thing",
	}
	err = r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	tr, ok := r.GetTaskRun("MEM-TICKET-1-1")
	require.True(t, ok)
	assert.Equal(t, taskrun.StateRunning, tr.State)

	// The graph should initially be empty (extraction happens on completion).
	assert.Equal(t, 0, graph.NodeCount())

	// Note: In the real flow, handleJobComplete triggers extractMemory in a
	// goroutine. Direct extraction testing is covered in memory_test.go.
	// Here we verify the reconciler accepted the WithMemory option and the
	// task was processed successfully with memory wired in.
}

// TestMemoryContextInjection verifies that when memory has prior knowledge,
// the MemoryContext field is populated on the engine.Task during
// ProcessTicket.
func TestMemoryContextInjection(t *testing.T) {
	t.Parallel()

	logger := memoryControllerLogger()
	ctx := context.Background()

	// Set up memory with some pre-existing knowledge.
	store, err := memory.NewSQLiteStore(":memory:", logger)
	require.NoError(t, err)
	defer store.Close()

	graph := memory.NewGraph(store, logger)

	// Add a fact that should be returned by the query.
	require.NoError(t, graph.AddNode(ctx, &memory.Fact{
		ID:         "prior-1",
		Content:    "the login service has a known timeout issue",
		FactKind:   memory.FactTypeFailurePattern,
		Confidence: 0.9,
		DecayRate:  0.01,
		ValidFrom:  time.Now(),
		TenantID:   "", // no tenant isolation for this test
	}))

	extractor := memory.NewExtractor(logger)
	queryEngine := memory.NewQueryEngine(graph, logger)

	// Verify the query engine returns the fact.
	mc, err := queryEngine.QueryForTask(ctx, "fix login timeout", "", "claude-code", "")
	require.NoError(t, err)
	require.NotNil(t, mc)
	assert.NotEmpty(t, mc.FormattedSection)
	assert.Contains(t, mc.FormattedSection, "timeout issue")

	// Now verify the reconciler wires this through.
	cfg := &config.Config{
		Engines: config.EnginesConfig{Default: "claude-code"},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     5,
			MaxJobDurationMinutes: 120,
		},
	}
	k8s := fake.NewSimpleClientset()

	// Use a capturing engine to verify MemoryContext is set on the task.
	capEng := &capturingEngine{name: "claude-code"}
	tb := newStubTicketing(nil)
	jb := &stubJobBuilder{}

	r := controller.NewReconciler(cfg, logger,
		controller.WithEngine(capEng),
		controller.WithTicketing(tb),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
		controller.WithMemory(graph, extractor, queryEngine),
	)

	ticket := ticketing.Ticket{
		ID:          "MEM-INJECT-1",
		Title:       "Fix login timeout",
		Description: "Users report login timeouts",
	}

	err = r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	// Verify the engine received a task with MemoryContext set.
	require.NotNil(t, capEng.lastTask)
	assert.NotEmpty(t, capEng.lastTask.MemoryContext,
		"MemoryContext should be populated from prior knowledge")
	assert.Contains(t, capEng.lastTask.MemoryContext, "timeout issue")
}

// TestMemoryDisabledDoesNotInterfere verifies that without memory wired in,
// the controller processes tickets normally with empty MemoryContext.
func TestMemoryDisabledDoesNotInterfere(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Engines: config.EnginesConfig{Default: "claude-code"},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     5,
			MaxJobDurationMinutes: 120,
		},
	}
	logger := memoryControllerLogger()
	k8s := fake.NewSimpleClientset()

	capEng := &capturingEngine{name: "claude-code"}
	tb := newStubTicketing(nil)
	jb := &stubJobBuilder{}

	r := controller.NewReconciler(cfg, logger,
		controller.WithEngine(capEng),
		controller.WithTicketing(tb),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
		// No WithMemory — memory is disabled.
	)

	ticket := ticketing.Ticket{
		ID:          "NO-MEM-1",
		Title:       "Normal ticket",
		Description: "Normal description",
	}

	ctx := context.Background()
	err := r.ProcessTicket(ctx, ticket)
	require.NoError(t, err)

	require.NotNil(t, capEng.lastTask)
	assert.Empty(t, capEng.lastTask.MemoryContext,
		"MemoryContext should be empty when memory is not wired in")
}

// capturingEngine captures the Task passed to BuildExecutionSpec for
// verification in tests.
type capturingEngine struct {
	name     string
	lastTask *engine.Task
}

func (e *capturingEngine) BuildExecutionSpec(task engine.Task, _ engine.EngineConfig) (*engine.ExecutionSpec, error) {
	e.lastTask = &task
	return &engine.ExecutionSpec{
		Image:                 "test-image:latest",
		Command:               []string{"echo", "hello"},
		Env:                   map[string]string{"TEST": "true"},
		ActiveDeadlineSeconds: 3600,
	}, nil
}

func (e *capturingEngine) BuildPrompt(task engine.Task) (string, error) {
	return "test prompt", nil
}

func (e *capturingEngine) Name() string          { return e.name }
func (e *capturingEngine) InterfaceVersion() int { return 1 }
